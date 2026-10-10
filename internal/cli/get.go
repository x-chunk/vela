package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/cli/output"
)

// progressThreshold: smaller files finish before a progress bar is worth
// drawing.
const progressThreshold = 1 << 20

func (a *App) getCommand() *cobra.Command {
	var recursive, force bool
	cmd := &cobra.Command{
		Use:   "get REMOTE [LOCAL]",
		Short: "Download files",
		Long: "Download a file (to the current directory by default, \"-\" for stdout), or with -r a\n" +
			"whole directory. Existing local files are never overwritten without --force, not even\n" +
			"one that appears while the download runs. Downloads are written to a temporary file,\n" +
			"verified against the SHA-256 and then moved into place. With -r nothing is written\n" +
			"outside LOCAL, even through symlinks inside it.",
		Example: "  vela get edu/math/q1.pdf\n  vela get edu/math/q1.pdf - | less\n  vela get -r edu/math/ ./math",
		Args:    rangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			local := ""
			if len(args) == 2 {
				local = args[1]
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			if recursive || strings.HasSuffix(args[0], "/") {
				if !recursive {
					return usagef("%s is a directory (use -r)", args[0])
				}
				return a.getTree(ctx, c, args[0], local, force)
			}
			remote, err := remotePath(args[0])
			if err != nil {
				return err
			}
			o, err := c.Stat(ctx, remote)
			if err != nil {
				err = a.notFoundHint(ctx, c, err, remote)
				var h *hinted
				if errors.As(err, &h) {
					h.hint = "it is a directory; use \"vela get -r " + remote + "/\""
				}
				return err
			}
			if local == "-" {
				return a.download(ctx, c, o, a.Out, false)
			}
			// The target directory is the user's choice and may be anything,
			// a symlink included; the file is created inside it, relative to
			// an opened handle.
			dir, name := ".", api.BaseName(o.Path)
			switch fi, err := os.Stat(local); {
			case local == "":
			case err == nil && fi.IsDir():
				dir = local
			default:
				dir, name = filepath.Dir(local), filepath.Base(local)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				return err
			}
			defer func() { _ = root.Close() }()
			if err := a.downloadFile(ctx, c, o, root, name, force); err != nil {
				return err
			}
			local = filepath.Join(dir, name)
			if a.jsonOut {
				return a.printer.JSONValue(o)
			}
			a.printer.Infof("%s -> %s (%s)", o.Path, local, output.Size(o.Size))
			return nil
		},
		ValidArgsFunction: a.completeRemote(),
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "download a directory recursively")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing local files")
	return cmd
}

func (a *App) getTree(ctx context.Context, c *client.Client, raw, local string, force bool) error {
	prefix, err := remotePrefix(raw)
	if err != nil {
		return err
	}
	if local == "" {
		local = "."
		if prefix != "" {
			local = api.BaseName(prefix)
		}
	}
	l, err := c.ListAll(ctx, client.ListOptions{Prefix: prefix, Recursive: true, Limit: 1000})
	if err != nil {
		return err
	}
	if len(l.Objects) == 0 {
		return &cliError{code: exitNotFound, slug: "not_found", msg: "no files under " + prefix}
	}
	// Every write goes through an os.Root opened on LOCAL: symlinks inside
	// it, existing or swapped in during the download, cannot lead outside.
	if err := os.MkdirAll(local, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(local)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	errs := make([]error, len(l.Objects))
	paths := make([]string, len(l.Objects))
	for i := range l.Objects {
		o := &l.Objects[i]
		paths[i] = o.Path
		rel := filepath.FromSlash(strings.TrimPrefix(o.Path, prefix))
		if !filepath.IsLocal(rel) {
			errs[i] = fmt.Errorf("refusing to write outside %s: %s", local, o.Path)
			continue
		}
		if dir := filepath.Dir(rel); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				errs[i] = err
				continue
			}
		}
		errs[i] = a.downloadFile(ctx, c, o, root, rel, force)
		if errs[i] == nil {
			a.printer.Infof("%s -> %s", o.Path, filepath.Join(local, rel))
		}
	}
	if err := batchResult("download", len(l.Objects), errs, paths); err != nil {
		for i, e := range errs {
			if e != nil && !a.jsonOut {
				a.printer.Errorf("failed:", "%s: %v", paths[i], e)
			}
		}
		return err
	}
	if a.jsonOut {
		return a.printer.JSONValue(l.Objects)
	}
	return nil
}

// downloadFile writes an object to name inside root: into a temporary file
// in the same directory, verified, then published. Without force the
// publication is a hard link, which fails atomically if name exists, so a
// file that appears during the download is never replaced; with force it is
// a rename over the target.
func (a *App) downloadFile(ctx context.Context, c *client.Client, o *api.Object, root *os.Root, name string, force bool) (err error) {
	display := filepath.Join(root.Name(), name)
	exists := func() error {
		return &cliError{code: exitConflict, slug: "already_exists", msg: "local file exists: " + display, hint: "use --force to overwrite"}
	}
	if !force {
		// Fail early in the common case; the publication below is the
		// authoritative, race-free check.
		if _, err := root.Lstat(name); err == nil {
			return exists()
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	tmp, tmpName, err := createTemp(root, filepath.Dir(name))
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = root.Remove(tmpName)
		}
	}()
	bar := !a.quiet && !a.jsonOut && a.ErrIsTerminal() && o.Size > progressThreshold
	if err := a.download(ctx, c, o, tmp, bar); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if force {
		return root.Rename(tmpName, name)
	}
	switch err := root.Link(tmpName, name); {
	case err == nil:
		_ = root.Remove(tmpName)
		return nil
	case errors.Is(err, fs.ErrExist):
		return exists()
	default:
		// Some filesystems have no hard links. Exclusive creation still
		// guarantees that nothing is replaced; the content is copied in.
		return copyExclusive(root, tmpName, name, exists)
	}
}

// copyExclusive copies src to a newly created dst inside root, failing if
// dst exists, and removes src.
func copyExclusive(root *os.Root, src, dst string, exists func() error) (err error) {
	out, err := root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666) // #nosec G302 -- umask applies
	if errors.Is(err, fs.ErrExist) {
		return exists()
	}
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = root.Remove(dst) // ours: created exclusively above
		}
	}()
	in, err := root.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return root.Remove(src)
}

// createTemp creates a hidden temporary file in dir inside root. Its name is
// short and fixed-length rather than derived from the target, so that a
// target name at the filesystem's length limit still works. Unlike
// os.CreateTemp, which always uses 0600, it requests 0666 and lets the
// user's umask decide, so a download gets the same permissions as any other
// file the user creates.
func createTemp(root *os.Root, dir string) (*os.File, string, error) {
	for range 100 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, "", err
		}
		name := filepath.Join(dir, ".vela-"+hex.EncodeToString(b[:])+".part")
		f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666) // #nosec G302 -- umask applies
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, name, err
	}
	return nil, "", fmt.Errorf("could not create a temporary file in %s", filepath.Join(root.Name(), dir))
}

// download streams an object to w and verifies its SHA-256 against the
// metadata, so a truncated or corrupted transfer is reported, not saved.
func (a *App) download(ctx context.Context, c *client.Client, o *api.Object, w io.Writer, bar bool) error {
	rc, _, err := c.Download(ctx, o.Path)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	dst := io.MultiWriter(w, h)
	var prog *output.Progress
	if bar {
		prog = output.NewProgress(a.Err, o.Path, o.Size)
		dst = io.MultiWriter(dst, prog.Writer())
	}
	_, err = io.Copy(dst, rc)
	if prog != nil {
		if err != nil {
			prog.Abort()
		} else {
			prog.Finish()
		}
	}
	if err != nil {
		return &client.NetworkError{Err: fmt.Errorf("download %s: %w", o.Path, err)}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != o.SHA256 {
		return fmt.Errorf("download %s: checksum mismatch (got %s, want %s); the file may have changed during the download", o.Path, got, o.SHA256)
	}
	return nil
}

func (a *App) catCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "cat REMOTE",
		Short:             "Print a file to stdout",
		Args:              exactArgs(1),
		ValidArgsFunction: a.completeRemote(),
		RunE: func(cmd *cobra.Command, args []string) error {
			remote, err := remotePath(args[0])
			if err != nil {
				return err
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			rc, _, err := c.Download(ctx, remote)
			if err != nil {
				return a.notFoundHint(ctx, c, err, remote)
			}
			defer func() { _ = rc.Close() }()
			if _, err := io.Copy(a.Out, rc); err != nil {
				return &client.NetworkError{Err: err}
			}
			return nil
		},
	}
}
