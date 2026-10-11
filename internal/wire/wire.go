// Package wire assembles the service from its concrete parts. It is the only
// package that knows both the service interfaces and their implementations,
// which keeps internal/service free of storage imports and lets the server
// binary and the test helpers build identical stacks.
package wire

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"go.xchunk.org/vela/internal/blob"
	"go.xchunk.org/vela/internal/clock"
	"go.xchunk.org/vela/internal/config"
	"go.xchunk.org/vela/internal/ids"
	"go.xchunk.org/vela/internal/r2"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/store"
)

// DatabaseFile is the SQLite file name inside the data directory.
const DatabaseFile = "vela.db"

// Storage bundles the opened persistence layers.
type Storage struct {
	// Kind identifies the active backend for metadata-only maintenance.
	Kind    string
	DB      *store.Store
	Blobs   *blob.Store
	Backend service.Blobs
}

// Close closes the database.
func (s *Storage) Close() error { return s.DB.Close() }

// OpenStorage opens the database and the blob store under dataDir and
// applies pending migrations. Every velad subcommand goes through here, so
// none of them can run against an outdated schema.
func OpenStorage(ctx context.Context, dataDir string, log *slog.Logger) (*Storage, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	bs, err := blob.Open(dataDir)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, filepath.Join(dataDir, DatabaseFile))
	if err != nil {
		return nil, err
	}
	from, to, err := db.Migrate(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if from != to && log != nil {
		log.Info("database migrated", "from_version", from, "to_version", to)
	}
	return &Storage{Kind: "fs", DB: db, Blobs: bs, Backend: blob.Backend{Store: bs}}, nil
}

// OpenConfiguredStorage selects the backend at runtime using the same binary.
func OpenConfiguredStorage(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Storage, error) {
	st, err := OpenStorage(ctx, cfg.DataDir, log)
	if err != nil {
		return nil, err
	}
	if cfg.Storage == "r2" {
		backend, err := r2.New(cfg.R2)
		if err == nil {
			probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = backend.Check(probeCtx)
			cancel()
		}
		if err != nil {
			_ = st.Close()
			return nil, err
		}
		st.Kind = "r2"
		st.Backend = r2WithTmp{Store: backend, local: st.Blobs}
		if log != nil {
			log.Info("R2 bucket access verified")
		}
	}
	return st, nil
}

// r2WithTmp preserves cleanup of abandoned local files after switching backends.
type r2WithTmp struct {
	*r2.Store
	local *blob.Store
}

func (s r2WithTmp) CleanTmp(cutoff time.Time) (int, error) { return s.local.CleanTmp(cutoff) }

// NewService builds a service over opened storage.
func NewService(st *Storage, clk clock.Clock, log *slog.Logger, opts service.Options) (*service.Service, error) {
	if clk == nil {
		clk = clock.System{}
	}
	return service.New(service.Deps{
		Store:  storeAdapter{st.DB},
		Blobs:  st.Backend,
		Clock:  clk,
		IDs:    ids.NewULID(clk.Now),
		Logger: log,
	}, opts)
}

// storeAdapter bridges the concrete transaction type to service.Writer; Go
// method signatures must match exactly, so this cannot be implicit.
type storeAdapter struct{ *store.Store }

func (a storeAdapter) Update(ctx context.Context, fn func(service.Writer) error) error {
	return a.Store.Update(ctx, func(tx *store.Tx) error { return fn(tx) })
}

var _ service.Store = storeAdapter{}
