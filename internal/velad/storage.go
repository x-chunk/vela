package velad

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/internal/blob"
	"go.xchunk.org/vela/internal/config"
	"go.xchunk.org/vela/internal/content"
	"go.xchunk.org/vela/internal/r2"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/wire"
)

func newStorageCommand(env Env) *cobra.Command {
	root := &cobra.Command{Use: "storage", Short: "Manage blob storage backends"}
	var from, to string
	var dryRun, deleteSource bool
	cmd := &cobra.Command{
		Use: "migrate --from fs|r2 --to fs|r2", Short: "Copy referenced blobs between backends without changing the database", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (from != "fs" && from != "r2") || (to != "fs" && to != "r2") || from == to {
				return errors.New("--from and --to must be different backends: fs and r2")
			}
			if dryRun && deleteSource {
				return errors.New("--dry-run cannot be combined with --delete-source")
			}
			cfg, err := loadConfig(cmd, env)
			if err != nil {
				return err
			}
			// Open both independently of the active backend; do not require source
			// credentials merely to use a local metadata snapshot.
			st, err := wire.OpenStorage(cmd.Context(), cfg.DataDir, adminLogger(cfg, env.Stderr))
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			svc, err := wire.NewService(st, nil, adminLogger(cfg, env.Stderr), migrationOptions(cfg))
			if err != nil {
				return err
			}
			ctx, release, err := svc.HoldBackup(cmd.Context())
			if err != nil {
				return err
			}
			defer release()
			source, err := migrationBackend(ctx, cfg, st, from)
			if err != nil {
				return err
			}
			target, err := migrationBackend(ctx, cfg, st, to)
			if err != nil {
				return err
			}
			refs, _, err := referencedBlobs(ctx, st.DB)
			if err != nil {
				return err
			}
			result, err := migrateBlobs(ctx, source, target, refs, dryRun, deleteSource, env.Stdout)
			_, _ = fmt.Fprintf(env.Stdout, "Migration: %d copied, %d skipped, %d failed, %d source blobs deleted.\n", result.copied, result.skipped, result.failed, result.deleted)
			return err
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "source backend")
	cmd.Flags().StringVar(&to, "to", "", "destination backend")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "verify sources and report planned copies without writing")
	cmd.Flags().BoolVar(&deleteSource, "delete-source", false, "delete source blobs only after every destination has been verified; stop writes first")
	root.AddCommand(cmd)
	return root
}

func migrationBackend(ctx context.Context, cfg *config.Config, st *wire.Storage, kind string) (content.Store, error) {
	if kind == "fs" {
		return blob.Backend{Store: st.Blobs}, nil
	}
	s, err := r2.New(cfg.R2)
	if err != nil {
		return nil, err
	}
	if err := s.Check(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

type migrationResult struct{ copied, skipped, failed, deleted int }

// migrateBlobs verifies existing destinations too: existence alone must not
// allow a corrupt object to be skipped or its last good source to be deleted.
func migrateBlobs(ctx context.Context, source, target content.Store, refs map[string]blobRef, dryRun, deleteSource bool, out io.Writer) (migrationResult, error) {
	var result migrationResult
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var errs []error
	for i, id := range ids {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		ref := refs[id]
		_, _ = fmt.Fprintf(out, "[%d/%d] %s ", i+1, len(ids), id)
		err := verifyStoredBlob(ctx, target, id, ref)
		switch {
		case err == nil:
			result.skipped++
			_, _ = fmt.Fprintln(out, "verified; skipped")
			continue
		case errors.Is(err, content.ErrNotFound):
			err = verifyStoredBlob(ctx, source, id, ref)
			if err == nil && dryRun {
				_, _ = fmt.Fprintln(out, "would copy")
				continue
			}
			if err == nil {
				var reader io.ReadCloser
				reader, err = source.Open(ctx, id, 0, -1)
				if err == nil {
					_, err = target.Put(ctx, id, reader, content.PutOptions{MaxSize: ref.size, SizeHint: ref.size, ExpectSHA256: ref.sha256})
					_ = reader.Close()
				}
				if err == nil {
					err = verifyStoredBlob(ctx, target, id, ref)
				}
				if err == nil {
					result.copied++
					_, _ = fmt.Fprintln(out, "copied and verified")
					continue
				}
			}
		}

		result.failed++
		errs = append(errs, fmt.Errorf("blob %s: %w", id, err))
		_, _ = fmt.Fprintln(out, "failed")
	}
	if len(errs) > 0 {
		return result, errors.Join(errs...)
	}
	if deleteSource {
		// Recheck all destinations before the first destructive step. Operators
		// must quiesce writes and keep the source backend stopped while deleting.
		for _, id := range ids {
			if err := verifyStoredBlob(ctx, target, id, refs[id]); err != nil {
				return result, fmt.Errorf("verify before source deletion: %w", err)
			}
		}
		for _, id := range ids {
			if err := source.Delete(ctx, id); err != nil {
				return result, fmt.Errorf("delete source blob %s: %w", id, err)
			}
			result.deleted++
		}
	}
	return result, nil
}

func verifyStoredBlob(ctx context.Context, s content.Store, id string, ref blobRef) error {
	reader, err := s.Open(ctx, id, 0, -1)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	h := sha256.New()
	// Read one extra byte to detect corrupt sizes without downloading an
	// arbitrarily large object from an accidentally reused bucket prefix.
	n, err := io.Copy(h, io.LimitReader(reader, ref.size+1))
	if err != nil {
		return err
	}
	if n != ref.size || hex.EncodeToString(h.Sum(nil)) != ref.sha256 {
		return errors.New("stored size or SHA-256 differs from SQLite")
	}
	return nil
}

func migrationOptions(cfg *config.Config) service.Options {
	return service.Options{MaxUpload: cfg.MaxUpload, MaxCreate: cfg.MaxCreate, LinkDefaultTTL: cfg.LinkDefaultTTL, LinkMaxTTL: cfg.LinkMaxTTL, DefaultVisibility: cfg.DefaultVisibility}
}
