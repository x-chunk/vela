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
			"whole directory. Existing local files are not overwritten without --force. Downloads\n" +
			"are written to a temporary file, verified against the SHA-256 and then renamed.",
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
			if local == "" {
				local = filepath.Base(filepath.FromSlash(o.Path))
			} else if fi, err := os.Stat(local); err == nil && fi.IsDir() {
				local = filepath.Join(local, api.BaseName(o.Path))
			}
			if err := a.downloadFile(ctx, c, o, local, force); err != nil {
				return err
			}
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
	root, err := filepath.Abs(local)
	if err != nil {
		return err
	}
	errs := make([]error, len(l.Objects))
	paths := make([]string, len(l.Objects))
	for i := range l.Objects {
		o := &l.Objects[i]
		paths[i] = o.Path
		dst := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(o.Path, prefix)))
		// Server paths are validated, but a local write outside the target
		// directory would be serious enough to check anyway.
		if rel, err := filepath.Rel(root, dst); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			errs[i] = fmt.Errorf("refusing to write outside %s: %s", root, o.Path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			errs[i] = err
			continue
		}
		errs[i] = a.downloadFile(ctx, c, o, dst, force)
		if errs[i] == nil {
			a.printer.Infof("%s -> %s", o.Path, dst)
		}
	}
	if err := batchResult("download", len(l.Objects), errs, paths); err != nil {
		for i, e := range errs {
			if e != nil && !a.jsonOut {
				_, _ = fmt.Fprintf(a.Err, "%s %s: %v\n", a.printer.Error("failed:"), paths[i], e)
			}
		}
		return err
	}
	if a.jsonOut {
		return a.printer.JSONValue(l.Objects)
	}
	return nil
}

// downloadFile writes an object to a local path atomically: into a
// temporary file next to it, verified, then renamed over the target.
func (a *App) downloadFile(ctx context.Context, c *client.Client, o *api.Object, local string, force bool) (err error) {
	if !force {
		if _, err := os.Lstat(local); err == nil {
			return &cliError{code: exitConflict, slug: "already_exists", msg: "local file exists: " + local, hint: "use --force to overwrite"}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	tmp, err := createTempLike(local)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	bar := !a.quiet && !a.jsonOut && a.ErrIsTerminal() && o.Size > progressThreshold
	if err := a.download(ctx, c, o, tmp, bar); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), local)
}

// createTempLike creates a hidden temporary file next to path. Unlike
// os.CreateTemp, which always uses 0600, it requests 0666 and lets the
// user's umask decide, so a download gets the same permissions as any other
// file the user creates.
func createTempLike(path string) (*os.File, error) {
	dir, base := filepath.Dir(path), filepath.Base(path)
	for range 100 {
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(dir, "."+base+".vela-"+hex.EncodeToString(b[:]))
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666) // #nosec G302 G304 -- umask applies; path chosen by the user
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, fmt.Errorf("could not create a temporary file next to %s", path)
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
