package velad

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
	"go.xchunk.org/vela/internal/store"
	"go.xchunk.org/vela/internal/wire"
)

func newBackupCommand(env Env) *cobra.Command {
	return &cobra.Command{
		Use:   "backup DEST_DIR",
		Short: "Write a consistent snapshot of the database and blobs",
		Long: "Write a consistent backup to DEST_DIR, which must not exist or be empty. The database is\n" +
			"copied with VACUUM INTO, which is safe while the server runs; then every blob the snapshot\n" +
			"references is hard-linked (or copied, across filesystems). Blobs are never modified in\n" +
			"place, so hard links are as good as copies and take no extra space.\n\n" +
			"To restore, stop the container and replace vela.db and blobs/ in the data directory with\n" +
			"the backup's files (removing vela.db-wal and vela.db-shm).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, st, _, err := openStorage(cmd, env)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			res, err := backup(cmd.Context(), st, args[0])
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(env.Stdout, "Backup written to %s: %d blob(s), %d linked, %d copied.\n",
				args[0], res.blobs, res.linked, res.copied)
			if res.missing > 0 {
				_, _ = fmt.Fprintf(env.Stderr, "warning: %d blob(s) were deleted while the backup ran; their objects were deleted too\n", res.missing)
			}
			return nil
		},
	}
}

type backupResult struct {
	blobs, linked, copied, missing int
}

func backup(ctx context.Context, st *wire.Storage, dest string) (backupResult, error) {
	var res backupResult
	if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
		return res, fmt.Errorf("%s is not empty", dest)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return res, fmt.Errorf("create %s: %w", dest, err)
	}
	dbPath := filepath.Join(dest, wire.DatabaseFile)
	if err := st.DB.Snapshot(ctx, dbPath); err != nil {
		return res, err
	}
	// The snapshot, not the live database, decides which blobs belong to
	// the backup: blobs created later are not referenced by it, and blobs
	// deleted meanwhile belonged to objects that were deleted too.
	snap, err := store.Open(ctx, dbPath)
	if err != nil {
		return res, err
	}
	ids, err := referencedBlobs(ctx, snap)
	_ = snap.Close()
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}
	if err != nil {
		return res, err
	}
	for _, id := range ids {
		rel := filepath.FromSlash(st.Blobs.RelPath(id))
		src := filepath.Join(st.Blobs.Dir(), rel)
		dst := filepath.Join(dest, "blobs", rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return res, err
		}
		res.blobs++
		if err := os.Link(src, dst); err == nil {
			res.linked++
			continue
		} else if errors.Is(err, fs.ErrNotExist) {
			res.blobs--
			res.missing++
			continue
		}
		if err := copyFile(src, dst); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				res.blobs--
				res.missing++
				continue
			}
			return res, err
		}
		res.copied++
	}
	return res, nil
}

func referencedBlobs(ctx context.Context, s *store.Store) ([]string, error) {
	objs, err := s.ObjectsUnder(ctx, "")
	if err != nil {
		return nil, err
	}
	trash, err := s.ListTrash(ctx, domain.TrashQuery{})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(objs)+len(trash))
	for _, o := range objs {
		ids = append(ids, o.BlobID)
	}
	for _, e := range trash {
		ids = append(ids, e.Object.BlobID)
	}
	return ids, nil
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src) // #nosec G304 -- path built from a validated blob ID.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G304 -- backup destination chosen by the operator.
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
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
