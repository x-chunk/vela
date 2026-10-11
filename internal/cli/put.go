package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/cli/output"
	"go.xchunk.org/vela/internal/units"
)

// putItem is one planned upload.
type putItem struct {
	Local  string `json:"local"`
	Remote string `json:"remote"`
	Size   int64  `json:"size"`
	stdin  bool
}

func (a *App) putCommand() *cobra.Command {
	var (
		m                                metaFlags
		force, dryRun, hidden, recursive bool
		jobs                             int
	)
	cmd := &cobra.Command{
		Use:   "put LOCAL... [REMOTE]",
		Short: "Upload files",
		Long: "Upload files. The last argument is the remote destination when there are several:\n" +
			"a directory (ending with /) or, for a single file, the exact remote path. With one\n" +
			"argument the file goes to the root. Existing files are never overwritten without --force.\n\n" +
			"With -r, directories are uploaded recursively: their contents land below the remote\n" +
			"directory, keeping the structure. Symlinks are skipped, and so are hidden files unless\n" +
			"--hidden is given. Use - as LOCAL to read from stdin.",
		Example: "  vela put report.pdf                      # -> report.pdf\n" +
			"  vela put report.pdf docs/math/            # -> docs/math/report.pdf\n" +
			"  vela put report.pdf docs/math/q1.pdf      # -> docs/math/q1.pdf\n" +
			"  vela put a.pdf b.pdf docs/                # several files into a directory\n" +
			"  vela put -r ./site docs/site/ --public    # a whole directory\n" +
			"  pg_dump db | vela put - backups/db.sql --private --expires 7d",
		Args: minArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobs < 1 {
				return usagef("-j must be at least 1")
			}
			plan, err := a.planPut(args, recursive, hidden)
			if err != nil {
				return err
			}
			if dryRun {
				return a.printPlan(plan)
			}
			c, s, err := a.client()
			if err != nil {
				return err
			}
			md, err := m.parse(cmd, s, a.Now())
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			return a.runPut(ctx, c, plan, md, force, jobs)
		},
	}
	m.register(cmd)
	f := cmd.Flags()
	f.BoolVar(&force, "force", false, "overwrite existing files")
	f.BoolVar(&dryRun, "dry-run", false, "show what would be uploaded and where")
	f.BoolVar(&hidden, "hidden", false, "include hidden files (.name) with -r")
	f.BoolVarP(&recursive, "recursive", "r", false, "upload directories recursively")
	f.IntVarP(&jobs, "jobs", "j", 4, "parallel uploads")
	return cmd
}

// planPut resolves arguments into uploads following the rules in the help
// text, validating every remote path before anything is sent.
func (a *App) planPut(args []string, recursive, hidden bool) ([]putItem, error) {
	sources, dest := args, ""
	if len(args) > 1 {
		sources, dest = args[:len(args)-1], args[len(args)-1]
	}
	dest = strings.TrimLeft(dest, "/")
	destIsDir := dest == "" || strings.HasSuffix(dest, "/")
	if len(sources) > 1 && !destIsDir {
		return nil, usagef("with several sources the destination must be a directory ending with /, got %q", dest)
	}
	var plan []putItem
	stdinUsed := false
	for _, src := range sources {
		if src == "-" {
			if stdinUsed {
				return nil, usagef("stdin (-) can only be uploaded once")
			}
			if destIsDir {
				return nil, usagef("uploading stdin needs a full remote path, e.g. vela put - notes/x.txt")
			}
			stdinUsed = true
			plan = append(plan, putItem{Local: "-", Remote: dest, Size: -1, stdin: true})
			continue
		}
		info, err := os.Stat(src)
		if err != nil {
			return nil, usageError(err)
		}
		switch {
		case info.IsDir():
			if !recursive {
				return nil, usagef("%s is a directory (use -r)", src)
			}
			if !destIsDir {
				return nil, usagef("the destination for a directory must end with /, got %q", dest)
			}
			items, err := a.walkDir(src, dest, hidden)
			if err != nil {
				return nil, err
			}
			plan = append(plan, items...)
		case info.Mode().IsRegular():
			remote := dest
			if destIsDir {
				remote = dest + filepath.Base(src)
			}
			plan = append(plan, putItem{Local: src, Remote: remote, Size: info.Size()})
		default:
			return nil, usagef("%s is not a regular file", src)
		}
	}
	seen := map[string]string{}
	for i, it := range plan {
		p, err := api.CleanObjectPath(it.Remote)
		if err != nil {
			return nil, usageError(fmt.Errorf("%s: %w", it.Local, err))
		}
		plan[i].Remote = p
		if prev, dup := seen[p]; dup {
			return nil, usagef("%s and %s would both upload to %s", prev, it.Local, p)
		}
		seen[p] = it.Local
	}
	if len(plan) == 0 {
		return nil, usageError(errNothingToDo)
	}
	return plan, nil
}

func (a *App) walkDir(root, dest string, hidden bool) ([]putItem, error) {
	var items []putItem
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != root && !hidden && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			a.printer.Warnf("skipping symlink %s", p)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			a.printer.Warnf("skipping special file %s", p)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		items = append(items, putItem{Local: p, Remote: dest + filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, usageError(err)
	}
	return items, nil
}

func (a *App) printPlan(plan []putItem) error {
	if a.jsonOut {
		return a.printer.JSONValue(plan)
	}
	var total int64
	for _, it := range plan {
		size := "stdin"
		if it.Size >= 0 {
			size = units.FormatSize(it.Size)
			total += it.Size
		}
		a.printer.Printf("would upload %s -> %s (%s)\n", it.Local, it.Remote, size)
	}
	a.printer.Infof("%d file(s), %s", len(plan), units.FormatSize(total))
	return nil
}

func (a *App) runPut(ctx context.Context, c *client.Client, plan []putItem, md meta, force bool, jobs int) error {
	results := make([]*api.Object, len(plan))
	errs := make([]error, len(plan))
	showBar := len(plan) == 1 && !a.quiet && !a.jsonOut && a.ErrIsTerminal()

	var wg sync.WaitGroup
	sem := make(chan struct{}, jobs)
	for i := range plan {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i], errs[i] = a.uploadOne(ctx, c, plan[i], md, force, showBar && plan[i].Size > progressThreshold)
			if len(plan) > 1 && errs[i] == nil {
				a.printer.Infof("uploaded %s", plan[i].Remote)
			}
		}(i)
	}
	wg.Wait()

	var done []api.Object
	paths := make([]string, len(plan))
	for i, it := range plan {
		paths[i] = it.Remote
		if errs[i] == nil {
			done = append(done, *results[i])
		}
	}
	batchErr := batchResult("upload", len(plan), errs, paths)
	if a.jsonOut {
		if batchErr != nil {
			var ce *cliError
			if errors.As(batchErr, &ce) {
				ce.details["objects"] = done
			}
			return batchErr
		}
		return a.printer.JSONValue(done)
	}
	for _, o := range done {
		a.printResultURL(o)
	}
	if batchErr != nil && len(plan) > 1 {
		for i, err := range errs {
			if err != nil {
				a.printer.Errorf("failed:", "%s: %v", paths[i], err)
			}
		}
	}
	return batchErr
}

// printResultURL prints where an uploaded object can be reached.
func (a *App) printResultURL(o api.Object) {
	if o.URL != nil {
		a.printer.Println(*o.URL)
		return
	}
	a.printer.Printf("%s %s\n", o.Path, a.printer.Dim("(private; share it with: vela share "+o.Path+")"))
}

func (a *App) uploadOne(ctx context.Context, c *client.Client, it putItem, md meta, force, bar bool) (*api.Object, error) {
	opts := client.UploadOptions{
		Size:        it.Size,
		ContentType: md.contentType,
		Visibility:  md.visibility,
		Title:       md.title,
		Description: md.description,
		Tags:        md.tags,
		Disposition: md.disposition,
		ExpiresAt:   md.expiresAt,
		CreateOnly:  !force,
	}
	var body io.Reader
	if it.stdin {
		body = a.In
	} else {
		f, err := os.Open(it.Local)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		// Hash first, then stream: the file can be read twice, and sending
		// the digest lets the server reject a corrupted transfer before the
		// object becomes visible.
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", it.Local, err)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		opts.Size = n
		opts.SHA256 = hex.EncodeToString(h.Sum(nil))
		body = f
	}
	var prog *output.Progress
	if bar {
		prog = output.NewProgress(a.Err, it.Remote, opts.Size)
		body = prog.Reader(body)
	}
	o, _, err := c.Upload(ctx, it.Remote, body, opts)
	if prog != nil {
		if err != nil {
			prog.Abort()
		} else {
			prog.Finish()
		}
	}
	if err != nil {
		err = hintFor(err, api.CodeAlreadyExists, "use --force to overwrite")
		err = hintFor(err, api.CodePathConflict, "a file and a directory cannot share a name")
		return nil, fmt.Errorf("%s: %w", it.Remote, err)
	}
	return o, nil
}
