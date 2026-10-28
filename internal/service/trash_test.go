package service_test

import (
	"fmt"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

func TestDeleteAndRestoreKeepsObjectID(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	o := e.put("docs/a.pdf", "a", api.Private)
	res, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "docs/a.pdf"})
	if err != nil || res.Deleted != 1 || len(res.TrashIDs) != 1 {
		t.Fatalf("delete = %+v, %v", res, err)
	}
	list, _ := e.svc.ListTrash(e.ctx, e.admin, service.TrashParams{})
	if len(list.Entries) != 1 || !list.Entries[0].PurgeAt.Equal(t0.Add(14*api.Day)) || list.Entries[0].Object.URL != nil {
		t.Fatalf("trash = %+v", list)
	}

	restored, err := e.svc.Restore(e.ctx, e.admin, res.TrashIDs[0], api.RestoreRequest{})
	if err != nil || restored.ID != o.ID || restored.Path != "docs/a.pdf" {
		t.Fatalf("restore = %+v, %v", restored, err)
	}
	_, err = e.svc.Restore(e.ctx, e.admin, res.TrashIDs[0], api.RestoreRequest{})
	wantErr(t, err, service.ErrNotFound)
}

func TestRestoreConflicts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("f", "old", api.Public)
	res, _ := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "f"})
	e.put("f", "new", api.Public)
	_, err := e.svc.Restore(e.ctx, e.admin, res.TrashIDs[0], api.RestoreRequest{})
	wantErr(t, err, service.ErrAlreadyExists)
	o, err := e.svc.Restore(e.ctx, e.admin, res.TrashIDs[0], api.RestoreRequest{Path: "g"})
	if err != nil || o.Path != "g" {
		t.Fatalf("restore elsewhere = %+v, %v", o, err)
	}
	res, _ = e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "g"})
	e.put("g/child", "c", api.Public)
	_, err = e.svc.Restore(e.ctx, e.admin, res.TrashIDs[0], api.RestoreRequest{})
	wantErr(t, err, service.ErrPathConflict)
	if _, err := e.svc.Restore(e.ctx, e.admin, res.TrashIDs[0], api.RestoreRequest{Path: "f", Overwrite: true}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteDirectories(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("d/1", "1", api.Public)
	e.put("d/2/3", "3", api.Public)
	_, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "d/"})
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "d"})
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "/", Recursive: true})
	wantErr(t, err, service.ErrInvalidRequest)
	res, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "d", Recursive: true})
	if err != nil || res.Deleted != 2 {
		t.Fatalf("recursive delete = %+v, %v", res, err)
	}
	_, err = e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "d/", Recursive: true})
	wantErr(t, err, service.ErrNotFound)
}

func TestPurge(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("p1", "1", api.Public)
	e.put("p2", "2", api.Public)
	e.put("p3", "3", api.Public)
	rw := e.principal(api.ScopeRW, "")
	_, err := e.svc.Delete(e.ctx, rw, service.DeleteParams{Path: "p1", Purge: true})
	wantErr(t, err, service.ErrForbidden)

	res, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "p1", Purge: true})
	if err != nil || res.Deleted != 1 || len(res.TrashIDs) != 0 {
		t.Fatalf("purge = %+v, %v", res, err)
	}

	d2, _ := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "p2"})
	e.clock.Advance(3 * api.Day)
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "p3"}); err != nil {
		t.Fatal(err)
	}
	err = e.svc.PurgeTrashEntry(e.ctx, rw, d2.TrashIDs[0])
	wantErr(t, err, service.ErrForbidden)

	older := 2 * api.Day
	n, err := e.svc.PurgeTrash(e.ctx, e.admin, &older)
	if err != nil || n.Purged != 1 {
		t.Fatalf("purge older than 2d = %+v, %v", n, err)
	}
	n, err = e.svc.PurgeTrash(e.ctx, e.admin, nil)
	if err != nil || n.Purged != 1 {
		t.Fatalf("purge all = %+v, %v", n, err)
	}
	if blobs := e.countFiles("blobs"); blobs != 3 {
		t.Fatalf("purged content deleted before the grace period: %d blobs", blobs)
	}
	e.collectAfterGrace()
	if blobs := e.countFiles("blobs"); blobs != 0 {
		t.Fatalf("blobs left after collection: %d", blobs)
	}
}

func TestTrashRespectsKeyPrefix(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("docs/a", "a", api.Public)
	e.put("other/b", "b", api.Public)
	resA, _ := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "docs/a"})
	resB, _ := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "other/b"})
	rw := e.principal(api.ScopeRW, "docs/")
	list, err := e.svc.ListTrash(e.ctx, rw, service.TrashParams{})
	if err != nil || len(list.Entries) != 1 || list.Entries[0].OriginalPath != "docs/a" {
		t.Fatalf("prefixed trash list = %+v, %v", list, err)
	}
	_, err = e.svc.Restore(e.ctx, rw, resB.TrashIDs[0], api.RestoreRequest{})
	wantErr(t, err, service.ErrNotFound)
	_, err = e.svc.Restore(e.ctx, rw, resA.TrashIDs[0], api.RestoreRequest{Path: "other/a"})
	wantErr(t, err, service.ErrForbidden)
	if _, err := e.svc.Restore(e.ctx, rw, resA.TrashIDs[0], api.RestoreRequest{}); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.ListTrash(e.ctx, e.principal(api.ScopeRO, ""), service.TrashParams{})
	wantErr(t, err, service.ErrForbidden)
}

// TestDeleteDirectoryIgnoresNoPrecondition covers R-017: If-Match is
// refused for a directory in both spellings, and nothing is deleted.
func TestDeleteDirectoryIgnoresNoPrecondition(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("tree/file", "x", api.Public)
	for _, p := range []string{"tree", "tree/"} {
		for _, purge := range []bool{false, true} {
			_, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: p, Recursive: true, Purge: purge, IfMatch: 999})
			wantErr(t, err, service.ErrInvalidRequest)
		}
	}
	if _, err := e.svc.Stat(e.ctx, e.admin, "tree/file"); err != nil {
		t.Fatalf("object deleted despite the refused precondition: %v", err)
	}
}

// TestRestoreByPathTakesTheLatestDeletion covers R-030: within one
// millisecond, the most recent deletion of a path is still the one listed
// first (and restored by path), and pagination is stable.
func TestRestoreByPathTakesTheLatestDeletion(t *testing.T) {
	t.Parallel()
	e := newEnv(t) // the fake clock stands still: every deletion shares one millisecond
	var ids []string
	for i := range 20 {
		e.put("same.txt", fmt.Sprintf("version %d", i), api.Public)
		res, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "same.txt"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.TrashIDs[0])
	}
	list, err := e.svc.ListTrash(e.ctx, e.admin, service.TrashParams{Path: "same.txt", Limit: 1})
	if err != nil || list.Entries[0].ID != ids[19] {
		t.Fatalf("newest entry = %v, want %s", list.Entries, ids[19])
	}
	var paged []string
	cursor := ""
	for {
		page, err := e.svc.ListTrash(e.ctx, e.admin, service.TrashParams{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, en := range page.Entries {
			paged = append(paged, en.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	for i, id := range paged {
		if id != ids[19-i] {
			t.Fatalf("page order differs at %d", i)
		}
	}
	_, err = e.svc.ListTrash(e.ctx, e.admin, service.TrashParams{Cursor: "bm9wZQ"})
	wantErr(t, err, service.ErrInvalidRequest)
}
