package service_test

import (
	"errors"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

func TestMoveSingle(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.put("a.txt", "a", api.Public)
	e.put("b.txt", "b", api.Public)

	n, err := e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "a.txt", To: "dir/a.txt"})
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	moved, _ := e.svc.Stat(e.ctx, e.admin, "dir/a.txt")
	if moved.ID != a.ID || moved.Revision != 2 {
		t.Fatalf("moved = %+v", moved)
	}
	_, err = e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "dir/a.txt", To: "b.txt"})
	wantErr(t, err, service.ErrAlreadyExists)
	if _, err := e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "dir/a.txt", To: "b.txt", Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	trash, _ := e.svc.ListTrash(e.ctx, e.admin, service.TrashParams{})
	if len(trash.Entries) != 1 || trash.Entries[0].OriginalPath != "b.txt" {
		t.Fatalf("overwritten object not trashed: %+v", trash)
	}
	_, err = e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "nope", To: "x"})
	wantErr(t, err, service.ErrNotFound)
	_, err = e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "b.txt", To: "b.txt"})
	wantErr(t, err, service.ErrInvalidRequest)
	// A file becoming the parent of its own new path is fine: it vacates.
	if _, err := e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "b.txt", To: "b.txt/inner"}); err != nil {
		t.Fatal(err)
	}
	e.put("file", "f", api.Public)
	_, err = e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "b.txt/inner", To: "file/x"})
	wantErr(t, err, service.ErrPathConflict)
}

func TestMoveRecursiveIsAtomic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("edu/math/q1.pdf", "1", api.Public)
	e.put("edu/math/sub/q2.pdf", "2", api.Public)
	e.put("school/math/q1.pdf", "taken", api.Public)

	_, err := e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "edu/math/", To: "school/math/", Recursive: true})
	var se *service.Error
	if !errors.As(err, &se) || !errors.Is(err, service.ErrAlreadyExists) {
		t.Fatalf("conflicting recursive move = %v", err)
	}
	if c, _ := se.Details["conflicts"].([]string); len(c) != 1 || c[0] != "school/math/q1.pdf" {
		t.Fatalf("conflicts = %+v", se.Details)
	}
	// Nothing moved.
	if _, err := e.svc.Stat(e.ctx, e.admin, "edu/math/sub/q2.pdf"); err != nil {
		t.Fatalf("partial move happened: %v", err)
	}

	n, err := e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "edu/math/", To: "school/math/", Recursive: true, Overwrite: true})
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	c, err := e.svc.Download(e.ctx, e.admin, "school/math/q1.pdf")
	if err != nil || e.read(c) != "1" {
		t.Fatalf("moved content: %v", err)
	}

	_, err = e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "school/", To: "school/inner/", Recursive: true})
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "", To: "x/", Recursive: true})
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "nothing/", To: "x/", Recursive: true})
	wantErr(t, err, service.ErrNotFound)
}

func TestMoveRecursiveOntoOwnSubtree(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// a/b/ -> a/ maps a/b/b/c onto a/b/c, which itself moves to a/c.
	e.put("a/b/c", "1", api.Public)
	e.put("a/b/b/c", "2", api.Public)
	n, err := e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "a/b/", To: "a/", Recursive: true})
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	for path, want := range map[string]string{"a/c": "1", "a/b/c": "2"} {
		c, err := e.svc.Download(e.ctx, e.admin, path)
		if err != nil || e.read(c) != want {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestMoveRespectsKeyPrefix(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("edu/a", "a", api.Public)
	rw := e.principal(api.ScopeRW, "edu/")
	_, err := e.svc.Move(e.ctx, rw, api.MoveRequest{From: "edu/a", To: "public/a"})
	wantErr(t, err, service.ErrForbidden)
	_, err = e.svc.Move(e.ctx, rw, api.MoveRequest{From: "edu/", To: "elsewhere/", Recursive: true})
	wantErr(t, err, service.ErrForbidden)
	if _, err := e.svc.Move(e.ctx, rw, api.MoveRequest{From: "edu/a", To: "edu/b"}); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Move(e.ctx, e.principal(api.ScopeRO, ""), api.MoveRequest{From: "edu/b", To: "edu/c"})
	wantErr(t, err, service.ErrForbidden)
}
