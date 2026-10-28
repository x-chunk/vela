// Package store is the SQLite implementation of the repositories used by
// internal/service.
//
// Connection strategy: two pools over the same database file. The writer pool
// has exactly one connection and opens transactions with BEGIN IMMEDIATE, so
// writers are serialized inside the process and take SQLite's write lock up
// front instead of failing with SQLITE_BUSY on lock upgrade. The reader pool
// has several query_only connections; in WAL mode they read a consistent
// snapshot concurrently with the writer. Other processes (velad subcommands
// run with docker exec) coordinate through SQLite locking and busy_timeout.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	// dbFileMode keeps the database, which holds signing keys, private to
	// the velad user.
	dbFileMode  = 0o600
	readerConns = 4
)

// Store is a SQLite-backed repository. Its exported read methods run on the
// reader pool; writes go through Update.
type Store struct {
	reads
	w *sql.DB
	r *sql.DB
}

// Open opens (creating if needed) the database at path. It does not run
// migrations; call Migrate.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("database path %q must not contain '?' or '#'", path)
	}
	// Create the file ourselves so that it starts with 0600. SQLite creates
	// the -wal and -shm files with the same permissions as the main file.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, dbFileMode) // #nosec G304 -- operator-configured path.
	if err != nil {
		return nil, fmt.Errorf("open database file: %w", err)
	}
	_ = f.Close()
	if err := os.Chmod(path, dbFileMode); err != nil {
		return nil, fmt.Errorf("chmod database file: %w", err)
	}

	common := "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	w, err := sql.Open("sqlite", path+"?"+common+"&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	// Ping the writer first: it switches the file to WAL mode, which is
	// persistent, before any reader connects.
	if err := w.PingContext(ctx); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	r, err := sql.Open("sqlite", path+"?"+common+"&_pragma=query_only(1)")
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	r.SetMaxOpenConns(readerConns)
	r.SetMaxIdleConns(readerConns)
	if err := r.PingContext(ctx); err != nil {
		_ = w.Close()
		_ = r.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &Store{reads: reads{db: r}, w: w, r: r}, nil
}

// Close closes both pools.
func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.w.Close())
}

// Ping checks that the database answers; used by /healthz.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	if err := s.r.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("database ping: %w", err)
	}
	return nil
}

// Tx is a write transaction. It offers every read method as well, operating
// on the transaction's view of the data.
type Tx struct {
	reads
	tx *sql.Tx
}

// Update runs fn in a write transaction, committing if fn returns nil and
// rolling back otherwise.
func (s *Store) Update(ctx context.Context, fn func(*Tx) error) error {
	sqlTx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	tx := &Tx{reads: reads{db: sqlTx}, tx: sqlTx}
	if err := fn(tx); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// Snapshot writes a consistent copy of the database to dst using
// VACUUM INTO, which is safe while the server is running. dst must not
// exist.
//
// The snapshot contains the signing keys, so it must be private from its
// first byte. SQLite would create the file with the umask's default mode;
// instead the empty file is created here with 0600, and VACUUM INTO, which
// accepts an existing empty file, keeps that mode. A failed snapshot is
// removed.
func (s *Store) Snapshot(ctx context.Context, dst string) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, dbFileMode) // #nosec G304 -- operator-chosen backup path
	if err != nil {
		return fmt.Errorf("create snapshot file: %w", err)
	}
	// The umask can only remove bits from 0600; chmod makes the mode exact.
	if err := errors.Join(f.Chmod(dbFileMode), f.Close()); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("create snapshot file: %w", err)
	}
	if _, err := s.w.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("snapshot database: %w", err)
	}
	return nil
}

// dbtx is the subset of *sql.DB and *sql.Tx the queries need.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// reads implements every read-only query against a pool or a transaction.
type reads struct {
	db dbtx
}

// Migrate applies pending migrations, each in its own transaction, and
// returns the schema version before and after. A database with a newer schema
// than this binary knows is refused, which protects against running an older
// velad after an upgrade.
func (s *Store) Migrate(ctx context.Context) (from, to int, err error) {
	if _, err := s.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT`); err != nil {
		return 0, 0, fmt.Errorf("create meta table: %w", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		return 0, 0, err
	}
	latest := 0
	if len(migrations) > 0 {
		latest = migrations[len(migrations)-1].version
	}
	from, err = s.schemaVersion(ctx)
	if err != nil {
		return 0, 0, err
	}
	if from > latest {
		return from, from, fmt.Errorf("database schema version %d is newer than this binary supports (%d); upgrade velad", from, latest)
	}
	to = from
	for _, m := range migrations {
		if m.version <= from {
			continue
		}
		err := s.Update(ctx, func(tx *Tx) error {
			if _, err := tx.tx.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("apply migration %s: %w", m.name, err)
			}
			_, err := tx.tx.ExecContext(ctx,
				`INSERT INTO meta (key, value) VALUES ('schema_version', ?)
				 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, strconv.Itoa(m.version))
			return err
		})
		if err != nil {
			return from, to, err
		}
		to = m.version
	}
	return from, to, nil
}

func (s *Store) schemaVersion(ctx context.Context) (int, error) {
	var v string
	err := s.w.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("corrupt schema version %q", v)
	}
	return n, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %q must be named NNNN_description.sql", e.Name())
		}
		b, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d", out[i].version)
		}
	}
	return out, nil
}

// Time helpers: the database stores Unix milliseconds.

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func nullMS(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func fromNullMS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

// mapErr converts driver errors into domain sentinels.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return fmt.Errorf("%w: %w", domain.ErrDuplicate, err)
		}
	}
	return err
}

// prefixUpper returns the smallest string greater than every string that
// starts with prefix, for range scans "path >= prefix AND path < upper". It
// increments the last byte; for directory prefixes that turns the trailing
// '/' into '0'. The last byte of valid UTF-8 is never 0xFF, so this cannot
// overflow for the paths Vela accepts. The empty prefix has no upper bound.
func prefixUpper(prefix string) string {
	if prefix == "" {
		return ""
	}
	b := []byte(prefix)
	b[len(b)-1]++
	return string(b)
}

// tagFilterSQL returns a condition on the JSON tag array of the table alias
// that holds when the tags match f (see api.TagFilter), with its arguments.
// f must not be empty. Tags are stored normalized, so plain equality is
// enough.
func tagFilterSQL(alias string, f api.TagFilter) (string, []any) {
	var args []any
	groups := make([]string, len(f))
	for i, g := range f {
		conds := make([]string, len(g))
		for j, tag := range g {
			conds[j] = `EXISTS (SELECT 1 FROM json_each(` + alias + `.tags) WHERE json_each.value = ?)`
			args = append(args, tag)
		}
		groups[i] = "(" + strings.Join(conds, " AND ") + ")"
	}
	return "(" + strings.Join(groups, " OR ") + ")", args
}
