package service_test

import (
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
	if err != nil || n != 1 {
		t.Fatalf("purge older than 2d = %d, %v", n, err)
	}
	n, err = e.svc.PurgeTrash(e.ctx, e.admin, nil)
	if err != nil || n != 1 {
		t.Fatalf("purge all = %d, %v", n, err)
	}
	if blobs := e.countFiles("blobs"); blobs != 0 {
		t.Fatalf("blobs left after purge: %d", blobs)
	}
}

func TestTrashRespectsKeyPrefix(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("edu/a", "a", api.Public)
	e.put("other/b", "b", api.Public)
	resA, _ := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "edu/a"})
	resB, _ := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "other/b"})
	rw := e.principal(api.ScopeRW, "edu/")
	list, err := e.svc.ListTrash(e.ctx, rw, service.TrashParams{})
	if err != nil || len(list.Entries) != 1 || list.Entries[0].OriginalPath != "edu/a" {
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
