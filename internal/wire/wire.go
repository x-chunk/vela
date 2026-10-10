// Package wire assembles the service from its concrete parts. It is the only
// package that knows both the service interfaces and their implementations,
// which keeps internal/service free of storage imports and lets the server
// binary and the test helpers build identical stacks.
package wire

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"go.xchunk.org/vela/internal/blob"
	"go.xchunk.org/vela/internal/clock"
	"go.xchunk.org/vela/internal/ids"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/store"
)

// DatabaseFile is the SQLite file name inside the data directory.
const DatabaseFile = "vela.db"

// Storage bundles the opened persistence layers.
type Storage struct {
	DB    *store.Store
	Blobs *blob.Store
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
	return &Storage{DB: db, Blobs: bs}, nil
}

// NewService builds a service over opened storage.
func NewService(st *Storage, clk clock.Clock, log *slog.Logger, opts service.Options) (*service.Service, error) {
	if clk == nil {
		clk = clock.System{}
	}
	return service.New(service.Deps{
		Store:  storeAdapter{st.DB},
		Blobs:  blobAdapter{st.Blobs},
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

// blobAdapter returns interface types where the blob store returns concrete
// ones.
type blobAdapter struct{ *blob.Store }

func (a blobAdapter) Create() (service.PendingBlob, error) {
	p, err := a.Store.Create()
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (a blobAdapter) Open(id string) (io.ReadSeekCloser, error) {
	f, err := a.Store.Open(id)
	if err != nil {
		return nil, err
	}
	return f, nil
}

var (
	_ service.Store = storeAdapter{}
	_ service.Blobs = blobAdapter{}
)
