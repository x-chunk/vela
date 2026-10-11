package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

func TestTrash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	var entries []domain.TrashEntry
	for i, p := range []string{"edu/a", "edu/b", "other/c", "edu/a"} {
		o := newObject(p, api.Public)
		e := domain.TrashEntry{ID: gen.New(), Object: o, DeletedAt: t0.Add(time.Duration(i) * time.Hour), DeletedBy: "k1"}
		entries = append(entries, e)
		time.Sleep(2 * time.Millisecond) // distinct ULID timestamps for ordering
	}
	err := s.Update(ctx, func(tx *Tx) error {
		for _, e := range entries {
			if err := tx.InsertTrash(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.TrashByID(ctx, entries[0].ID)
	if err != nil || got.Object.Path != "edu/a" || got.DeletedBy != "k1" || !got.DeletedAt.Equal(t0) {
		t.Fatalf("TrashByID = %+v, %v", got, err)
	}
	if ok, _ := s.BlobReferenced(ctx, entries[2].Object.BlobID); !ok {
		t.Fatal("trashed blob not referenced")
	}

	list, _ := s.ListTrash(ctx, domain.TrashQuery{})
	if len(list) != 4 || list[0].ID != entries[3].ID {
		t.Fatalf("ListTrash order wrong: %d entries, first %s", len(list), list[0].ID)
	}
	list, _ = s.ListTrash(ctx, domain.TrashQuery{Prefix: "edu/"})
	if len(list) != 3 {
		t.Fatalf("prefix filter: %d entries", len(list))
	}
	list, _ = s.ListTrash(ctx, domain.TrashQuery{Path: "edu/a"})
	if len(list) != 2 || list[0].ID != entries[3].ID {
		t.Fatalf("path filter: %+v", list)
	}
	list, _ = s.ListTrash(ctx, domain.TrashQuery{Within: "other/"})
	if len(list) != 1 {
		t.Fatalf("within filter: %d entries", len(list))
	}
	list, _ = s.ListTrash(ctx, domain.TrashQuery{BeforeSeq: 3, Limit: 1})
	if len(list) != 1 || list[0].ID != entries[1].ID {
		t.Fatalf("cursor: %+v", list)
	}

	old, _ := s.TrashDeletedBefore(ctx, t0.Add(90*time.Minute), "", 10)
	if len(old) != 2 || old[0].ID != entries[0].ID {
		t.Fatalf("TrashDeletedBefore = %+v", old)
	}
	old, _ = s.TrashDeletedBefore(ctx, t0.Add(24*time.Hour), "other/", 10)
	if len(old) != 1 {
		t.Fatalf("TrashDeletedBefore(within) = %d", len(old))
	}

	if err := s.Update(ctx, func(tx *Tx) error { return tx.DeleteTrash(ctx, entries[0].ID) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrashByID(ctx, entries[0].ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted entry still found: %v", err)
	}
}

// TestTrashSeqMigration checks that existing trash entries are numbered by
// deletion time, and that new entries continue the sequence.
func TestTrashSeqMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "vela.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.ExecContext(ctx, `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:2] {
		if _, err := s.w.ExecContext(ctx, m.sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.w.ExecContext(ctx, `INSERT INTO meta VALUES ('schema_version', '2')`); err != nil {
		t.Fatal(err)
	}
	// Inserted out of deletion order on purpose.
	for i, d := range []int64{300, 100, 200} {
		_, err := s.w.ExecContext(ctx, `INSERT INTO trash (id, object_id, original_path, blob_id, size, sha256, content_type,
			disposition, visibility, title, description, tags, created_at, updated_at, revision, deleted_at)
			VALUES (?, ?, 'p', ?, 0, '', 'x/y', 'inline', 'public', '', '', '[]', 0, 0, 1, ?)`,
			fmt.Sprintf("t%d", i), fmt.Sprintf("o%d", i), fmt.Sprintf("b%d", i), d)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListTrash(ctx, domain.TrashQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range list {
		got = append(got, fmt.Sprintf("%s:%d", e.ID, e.Seq))
	}
	if strings.Join(got, " ") != "t0:3 t2:2 t1:1" {
		t.Fatalf("migrated order = %v", got)
	}
	err = s.Update(ctx, func(tx *Tx) error {
		return tx.InsertTrash(ctx, domain.TrashEntry{ID: gen.New(), Object: newObject("q", api.Public), DeletedAt: t0})
	})
	if err != nil {
		t.Fatal(err)
	}
	if list, _ = s.ListTrash(ctx, domain.TrashQuery{Limit: 1}); list[0].Seq != 4 {
		t.Fatalf("new entry seq = %d", list[0].Seq)
	}
}
