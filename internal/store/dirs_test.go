package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

func TestDirectoryPolicyGenerationAndSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTest(t)
	rule := api.DirPolicy{Prefix: "a/", Index: api.IndexOn, Hidden: true, NoIndex: api.IndexInherit, Title: "Area", CreatedAt: t0, UpdatedAt: t0, Revision: 1}
	before, err := db.DirPoliciesVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fail := errors.New("rollback")
	err = db.Update(ctx, func(tx *Tx) error {
		if err := tx.PutDirPolicy(ctx, rule); err != nil {
			return err
		}
		return fail
	})
	if !errors.Is(err, fail) {
		t.Fatal(err)
	}
	after, err := db.DirPoliciesVersion(ctx)
	if err != nil || after != before {
		t.Fatal("rolled-back generation changed", after, err)
	}
	if _, err := db.DirPolicyByPrefix(ctx, "a/"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if err := db.Update(ctx, func(tx *Tx) error { return tx.PutDirPolicy(ctx, rule) }); err != nil {
		t.Fatal(err)
	}
	after, err = db.DirPoliciesVersion(ctx)
	if err != nil || after != before+1 {
		t.Fatal(after, err)
	}
	snapshot := filepath.Join(t.TempDir(), "backup.db")
	if err := db.Snapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	got, err := restored.DirPolicyByPrefix(ctx, "a/")
	if err != nil || got != rule {
		t.Fatal(got, err)
	}
	if err := db.Update(ctx, func(tx *Tx) error { return tx.DeleteDirPolicy(ctx, "a/") }); err != nil {
		t.Fatal(err)
	}
	after, err = db.DirPoliciesVersion(ctx)
	if err != nil || after != before+2 {
		t.Fatal(after, err)
	}
}

func TestDirectoryStatsRoot(t *testing.T) {
	t.Parallel()
	db := openTest(t)
	insert(t, db, newObject("a/file", api.Public), newObject("private", api.Private), newObject("unlisted", api.Unlisted))
	n, latest, err := db.DirStats(context.Background(), "", t0)
	if err != nil || n != 1 || !latest.Equal(t0) {
		t.Fatal(n, latest, err)
	}
}
