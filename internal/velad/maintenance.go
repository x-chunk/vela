package velad

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/store"
	"go.xchunk.org/vela/internal/wire"
)

func newBackupCommand(env Env) *cobra.Command {
	var verify bool
	cmd := &cobra.Command{
		Use:   "backup DEST_DIR",
		Short: "Write a consistent snapshot of the database and blobs",
		Long: "Write a consistent backup to DEST_DIR, which must not exist or be empty. The database is\n" +
			"copied with VACUUM INTO, which is safe while the server runs; then every blob the snapshot\n" +
			"references is hard-linked (or copied, across filesystems) and its size checked; --verify\n" +
			"also checks every SHA-256. While the backup runs, no blob is deleted, in this or any other\n" +
			"process. A missing or damaged blob makes the backup fail: it is never silently partial.\n\n" +
			"To restore, stop the container and replace vela.db and blobs/ in the data directory with\n" +
			"the backup's files (removing vela.db-wal and vela.db-shm).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, st, err := adminService(cmd, env)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			res, err := backup(cmd.Context(), svc, st, args[0], verify)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(env.Stdout, "Backup written to %s: %d object(s), %d blob(s): %d linked, %d copied%s.\n",
				args[0], res.objects, res.blobs, res.linked, res.copied, map[bool]string{true: ", all verified"}[verify])
			return nil
		},
	}
	cmd.Flags().BoolVar(&verify, "verify", false, "check the SHA-256 of every blob (reads all data)")
	return cmd
}

type backupResult struct {
	objects, blobs, linked, copied int
}

// backupAfterSnapshot is a test hook that runs between the database
// snapshot and the copying of blobs.
var backupAfterSnapshot func()

// blobRef is what the snapshot says a blob must contain.
type blobRef struct {
	path   string
	size   int64
	sha256 string
}

func backup(ctx context.Context, svc *service.Service, st *wire.Storage, dest string, verify bool) (res backupResult, err error) {
	if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
		return res, fmt.Errorf("%s is not empty", dest)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	// The backup holds the signing keys and every private file: make the
	// directory private before anything is written into it, also when the
	// operator created it beforehand with wider permissions.
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return res, fmt.Errorf("create %s: %w", dest, err)
	}
	if err := os.Chmod(dest, 0o700); err != nil { // #nosec G302 -- owner-only directory
		return res, fmt.Errorf("make %s private: %w", dest, err)
	}
	// From here on dest is ours; a failed backup leaves it empty, so it
	// cannot be mistaken for a complete one.
	dbPath := filepath.Join(dest, wire.DatabaseFile)
	blobsDir := filepath.Join(dest, "blobs")
	defer func() {
		if err != nil {
			_ = os.Remove(dbPath)
			_ = os.RemoveAll(blobsDir)
		}
	}()

	// Pin blobs before the snapshot: everything the snapshot references
	// stays on disk until the copies exist.
	ctx, release, err := svc.HoldBackup(ctx)
	if err != nil {
		return res, err
	}
	defer release()

	if err := st.DB.Snapshot(ctx, dbPath); err != nil {
		return res, err
	}
	snap, err := store.Open(ctx, dbPath)
	if err != nil {
		return res, err
	}
	refs, objects, err := referencedBlobs(ctx, snap)
	_ = snap.Close()
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}
	if err != nil {
		return res, err
	}
	res.objects = objects
	if st.Kind == "r2" {
		if verify {
			return res, errors.New("--verify is not supported for metadata-only R2 backups; verify the bucket backup separately")
		}
		return res, nil
	}

	if backupAfterSnapshot != nil {
		backupAfterSnapshot()
	}

	for id, ref := range refs {
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("backup interrupted: %w", context.Cause(ctx))
		}
		rel := filepath.FromSlash(st.Blobs.RelPath(id))
		src := filepath.Join(st.Blobs.Dir(), rel)
		dst := filepath.Join(blobsDir, rel)
		if err := mkdirsMode(blobsDir, filepath.Dir(rel), 0o755); err != nil {
			return res, err
		}
		linkErr := errLinkDisabled
		if !backupForceCopy {
			linkErr = os.Link(src, dst)
		}
		if err := linkErr; err == nil {
			res.linked++
		} else if errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("blob %s of %s is missing from the data directory; the backup is incomplete", id, ref.path)
		} else if err := copyFile(src, dst); err != nil {
			return res, fmt.Errorf("copy blob %s of %s: %w", id, ref.path, err)
		} else {
			res.copied++
		}
		if err := checkBlob(dst, ref, verify); err != nil {
			return res, fmt.Errorf("blob %s of %s: %w", id, ref.path, err)
		}
		res.blobs++
	}
	return res, nil
}

// backupForceCopy makes tests take the cross-filesystem copy path.
var backupForceCopy bool

var errLinkDisabled = errors.New("hard links disabled")

// mkdirsMode creates root/rel and every directory between them with exactly
// mode, whatever the umask: restored blob directories must be traversable
// by nginx, which runs as another user.
func mkdirsMode(root, rel string, mode os.FileMode) error {
	dir := root
	for _, part := range append([]string{""}, strings.Split(filepath.ToSlash(rel), "/")...) {
		if part != "" {
			dir = filepath.Join(dir, part)
		}
		if err := os.Mkdir(dir, mode); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := os.Chmod(dir, mode); err != nil {
			return err
		}
	}
	return nil
}

// checkBlob compares a copied blob with what the snapshot expects.
func checkBlob(path string, ref blobRef, full bool) error {
	f, err := os.Open(path) // #nosec G304 -- inside the backup directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() != ref.size {
		return fmt.Errorf("size %d, expected %d", fi.Size(), ref.size)
	}
	if !full {
		return nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != ref.sha256 {
		return fmt.Errorf("SHA-256 %s, expected %s", got, ref.sha256)
	}
	return nil
}

// referencedBlobs lists every blob the snapshot references, by objects and
// by trash entries, with the expected size and digest.
func referencedBlobs(ctx context.Context, s *store.Store) (map[string]blobRef, int, error) {
	objs, err := s.ObjectsUnder(ctx, "")
	if err != nil {
		return nil, 0, err
	}
	trash, err := s.ListTrash(ctx, domain.TrashQuery{})
	if err != nil {
		return nil, 0, err
	}
	refs := make(map[string]blobRef, len(objs)+len(trash))
	for _, o := range objs {
		refs[o.BlobID] = blobRef{path: o.Path, size: o.Size, sha256: o.SHA256}
	}
	for _, e := range trash {
		refs[e.Object.BlobID] = blobRef{path: e.Object.Path + " (in the trash)", size: e.Object.Size, sha256: e.Object.SHA256}
	}
	return refs, len(objs) + len(trash), nil
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src) // #nosec G304 -- path built from a validated blob ID.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	// Backed-up blobs keep the blob mode (0644) so that a restored data
	// directory is readable by nginx again without manual chmod.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302 G304 -- see above; destination chosen by the operator
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	// Explicit, because the umask may have removed the read bits for nginx.
	if err := out.Chmod(0o644); err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func newHealthcheckCommand(env Env) *cobra.Command {
	return &cobra.Command{
		Use:   "healthcheck",
		Short: "Probe the local server's /healthz (for Docker HEALTHCHECK)",
		Long:  "Exit 0 if the server listening on VELA_LISTEN reports healthy. Distroless images have no curl.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd, env)
			if err != nil {
				return err
			}
			target, err := healthURL(cfg.Listen)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			if err != nil {
				return err
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusOK {
				return fmt.Errorf("unhealthy: %s", res.Status)
			}
			return nil
		},
	}
}

// healthURL turns a listen address into a URL on the loopback interface.
func healthURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + api.RouteHealth, nil
}

func newMigrateCommand(env Env) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply database migrations (serve does this on start)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd, env)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
				return err
			}
			db, err := store.Open(cmd.Context(), filepath.Join(cfg.DataDir, wire.DatabaseFile))
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			from, to, err := db.Migrate(cmd.Context())
			if err != nil {
				return err
			}
			if from == to {
				_, _ = fmt.Fprintf(env.Stdout, "Schema is up to date (version %d).\n", to)
			} else {
				_, _ = fmt.Fprintf(env.Stdout, "Migrated schema from version %d to %d.\n", from, to)
			}
			return nil
		},
	}
}
