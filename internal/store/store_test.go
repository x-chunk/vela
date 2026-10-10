package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
	"go.xchunk.org/vela/internal/ids"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func openTest(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "vela.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

var gen = ids.NewULID(nil)

func newObject(path string, vis api.Visibility) domain.Object {
	return domain.Object{
		ID: gen.New(), Path: path, BlobID: gen.New(), Size: 1, SHA256: "00",
		ContentType: "text/plain", Disposition: api.Inline, Visibility: vis,
		Tags: []string{"a"}, CreatedAt: t0, UpdatedAt: t0, Revision: 1,
	}
}

func insert(t *testing.T, s *Store, objs ...domain.Object) {
	t.Helper()
	err := s.Update(context.Background(), func(tx *Tx) error {
		for _, o := range objs {
			if err := tx.InsertObject(context.Background(), o); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenPermissionsAndMigrate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vela.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	latest := migrations[len(migrations)-1].version
	from, to, err := s.Migrate(ctx)
	if err != nil || from != 0 || to != latest {
		t.Fatalf("Migrate = %d, %d, %v", from, to, err)
	}
	from, to, err = s.Migrate(ctx)
	if err != nil || from != latest || to != latest {
		t.Fatalf("second Migrate = %d, %d, %v", from, to, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("database mode = %o, want 600", fi.Mode().Perm())
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	// A schema from the future is refused.
	if _, err := s.w.ExecContext(ctx, `UPDATE meta SET value = '99' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Migrate(ctx); err == nil {
		t.Fatal("Migrate accepted a newer schema")
	}
}

func TestReaderIsReadOnly(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if _, err := s.r.ExecContext(context.Background(), `DELETE FROM objects`); err == nil {
		t.Fatal("reader pool accepted a write")
	}
}

func TestSnapshot(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	insert(t, s, newObject("a.txt", api.Public))
	dst := filepath.Join(t.TempDir(), "snap.db")
	if err := s.Snapshot(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
	snap, err := Open(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close() }()
	if _, err := snap.ObjectByPath(context.Background(), "a.txt"); err != nil {
		t.Fatalf("snapshot lacks object: %v", err)
	}
}

func TestPrefixUpper(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"": "", "a/": "a0", "edu/math/": "edu/math0", "ab": "ac"} {
		if got := prefixUpper(in); got != want {
			t.Errorf("prefixUpper(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestBlobQueueAndCheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	id := gen.New()
	var added [2]bool
	err := s.Update(ctx, func(tx *Tx) error {
		var err error
		if added[0], err = tx.EnqueueBlobDeletion(ctx, id, t0); err != nil {
			return err
		}
		added[1], err = tx.EnqueueBlobDeletion(ctx, id, t0.Add(time.Hour))
		return err
	})
	if err != nil || !added[0] || added[1] {
		t.Fatalf("enqueue = %v, %v", added, err)
	}
	if due, _ := s.DueBlobDeletions(ctx, t0.Add(-time.Second), 10); len(due) != 0 {
		t.Fatalf("due before its time: %v", due)
	}
	if due, _ := s.DueBlobDeletions(ctx, t0, 10); len(due) != 1 || due[0] != id {
		t.Fatalf("due = %v", due)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint on an idle database: %v", err)
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.DeleteBlobDeletion(ctx, id) }); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.DueBlobDeletions(ctx, t0, 10); len(due) != 0 {
		t.Fatalf("due after dequeue: %v", due)
	}
}

func TestSnapshotRefusesExistingFile(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	dst := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(dst, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshot(context.Background(), dst); err == nil {
		t.Fatal("snapshot overwrote an existing file")
	}
}
