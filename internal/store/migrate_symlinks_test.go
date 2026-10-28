package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

// migrateTo applies the embedded migrations up to version, as an older velad
// would have left the database.
func migrateTo(t *testing.T, s *Store, version int) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.w.ExecContext(ctx, `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version > version {
			break
		}
		if _, err := s.w.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("apply %s: %v", m.name, err)
		}
	}
	if _, err := s.w.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('schema_version', ?)`, strconv.Itoa(version)); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateSymlinksKeepsData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "vela.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	migrateTo(t, s, 5)

	// Schema 5 data: a live object with an expiry, a trash entry, a link and
	// a directory policy.
	for _, q := range []string{
		`INSERT INTO objects (id, path, blob_id, size, sha256, content_type, disposition, visibility, title, description, tags, created_at, updated_at, expires_at, revision)
		 VALUES ('o1', 'docs/a.pdf', 'b1', 3, 'aa', 'application/pdf', 'attachment', 'public', 'T', 'D', '["x"]', 1, 2, 9000000000000, 4)`,
		`INSERT INTO trash (id, object_id, original_path, blob_id, size, sha256, content_type, disposition, visibility, title, description, tags, created_at, updated_at, expires_at, revision, deleted_at, deleted_by_key, seq)
		 VALUES ('e1', 'o2', 'docs/old.txt', 'b2', 1, 'bb', 'text/plain', 'inline', 'private', '', '', '[]', 1, 2, NULL, 1, 3, 'k1', 1)`,
		`UPDATE counters SET value = 1 WHERE name = 'trash'`,
		`INSERT INTO signing_keys (id, secret, created_at) VALUES ('s1', x'00', 1)`,
		`INSERT INTO links (id, object_id, signing_key_id, filename, created_at, expires_at) VALUES ('l1', 'o1', 's1', 'a.pdf', 1, 2)`,
		`INSERT INTO dir_policies (prefix, idx, created_at, updated_at) VALUES ('docs/', 'off', 1, 1)`,
	} {
		if _, err := s.w.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	from, to, err := s.Migrate(ctx)
	if err != nil || from != 5 || to < 6 {
		t.Fatalf("Migrate = %d, %d, %v", from, to, err)
	}
	var check string
	if err := s.w.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q, %v", check, err)
	}

	o, err := s.ObjectByPath(ctx, "docs/a.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if o.ID != "o1" || o.Kind != domain.KindFile || o.TargetID != "" || o.BlobID != "b1" || o.Size != 3 ||
		o.SHA256 != "aa" || o.ContentType != "application/pdf" || o.Disposition != api.Attachment ||
		o.Visibility != api.Public || o.Title != "T" || o.Description != "D" || len(o.Tags) != 1 ||
		o.ExpiresAt == nil || o.Revision != 4 {
		t.Errorf("migrated object = %+v", o)
	}
	e, err := s.TrashByID(ctx, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Object.ID != "o2" || e.Object.Kind != domain.KindFile || e.Object.BlobID != "b2" || e.Seq != 1 || e.DeletedBy != "k1" {
		t.Errorf("migrated trash entry = %+v", e)
	}
	if _, err := s.LinkByID(ctx, "l1"); err != nil {
		t.Errorf("link lost: %v", err)
	}
	if _, err := s.DirPolicyByPrefix(ctx, "docs/"); err != nil {
		t.Errorf("directory policy lost: %v", err)
	}
	var indexes int
	if err := s.w.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN
		('objects_expires_at', 'objects_target_id', 'trash_original_path', 'trash_deleted_at', 'trash_seq')`).Scan(&indexes); err != nil || indexes != 5 {
		t.Errorf("indexes = %d, %v", indexes, err)
	}
	// The trash counter keeps numbering after the rebuild.
	insert(t, s, newObject("docs/b.txt", api.Private))
	b, err := s.ObjectByPath(ctx, "docs/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(ctx, func(tx *Tx) error {
		return tx.InsertTrash(ctx, domain.TrashEntry{ID: "e2", Object: b, DeletedAt: t0})
	})
	if err != nil {
		t.Fatal(err)
	}
	if e2, err := s.TrashByID(ctx, "e2"); err != nil || e2.Seq != 2 {
		t.Errorf("new trash entry = %+v, %v", e2, err)
	}
}

func TestSymlinkRowConstraints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	for name, q := range map[string]string{
		"symlink with blob":      `INSERT INTO objects (id, path, kind, target_id, blob_id, size, sha256, content_type, visibility, created_at, updated_at) VALUES ('x', 'x', 'symlink', 't', 'b', 0, '', '', 'public', 1, 1)`,
		"symlink without target": `INSERT INTO objects (id, path, kind, size, sha256, content_type, visibility, created_at, updated_at) VALUES ('x', 'x', 'symlink', 0, '', '', 'public', 1, 1)`,
		"file with target":       `INSERT INTO objects (id, path, kind, target_id, blob_id, size, sha256, content_type, visibility, created_at, updated_at) VALUES ('x', 'x', 'file', 't', 'b', 0, '', '', 'public', 1, 1)`,
		"file without blob":      `INSERT INTO objects (id, path, kind, size, sha256, content_type, visibility, created_at, updated_at) VALUES ('x', 'x', 'file', 0, '', '', 'public', 1, 1)`,
		"unknown kind":           `INSERT INTO objects (id, path, kind, blob_id, size, sha256, content_type, visibility, created_at, updated_at) VALUES ('x', 'x', 'dir', 'b', 0, '', '', 'public', 1, 1)`,
	} {
		if _, err := s.w.ExecContext(ctx, q); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// Symlinks round-trip with empty content fields, even if the caller
	// passes resolved values, and several of them share a NULL blob_id.
	target := newObject("a.txt", api.Public)
	l1 := domain.Object{ID: gen.New(), Path: "l1", Kind: domain.KindSymlink, TargetID: target.ID, Size: 5, SHA256: "ff",
		ContentType: "text/plain", Disposition: api.Inline, Visibility: api.Public, Tags: []string{}, CreatedAt: t0, UpdatedAt: t0, Revision: 1}
	l2 := l1
	l2.ID, l2.Path = gen.New(), "l2"
	insert(t, s, target, l1, l2)
	var size int64
	var sum, ct string
	if err := s.w.QueryRowContext(ctx, `SELECT size, sha256, content_type FROM objects WHERE id = ?`, l1.ID).Scan(&size, &sum, &ct); err != nil {
		t.Fatal(err)
	}
	if size != 0 || sum != "" || ct != "" {
		t.Errorf("stored symlink content fields = %d %q %q", size, sum, ct)
	}
	err := s.Update(ctx, func(tx *Tx) error {
		return tx.InsertTrash(ctx, domain.TrashEntry{ID: gen.New(), Object: l1, DeletedAt: t0})
	})
	if err != nil {
		t.Fatalf("trash a symlink: %v", err)
	}
}
