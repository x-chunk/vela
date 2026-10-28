package service_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

// ln creates a symlink as p and fails the test on error.
func (e *env) ln(p service.Principal, source, path string) api.Object {
	e.t.Helper()
	o, _, err := e.svc.CreateSymlink(e.ctx, p, api.CreateSymlinkRequest{Source: source, Path: path})
	if err != nil {
		e.t.Fatalf("ln %s %s: %v", source, path, err)
	}
	return o
}

func TestCreateSymlinkCopiesMetadata(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	orig := e.put("orig/a.txt", "hello", api.Public)
	expires := t0.Add(48 * time.Hour)
	_, err := e.svc.Patch(e.ctx, e.admin, "orig/a.txt", api.ObjectPatch{
		Title: api.Some("Title"), Description: api.Some("Desc"), Tags: api.Some([]string{"x", "y"}),
		Disposition: api.Some(api.Attachment), ExpiresAt: api.Some(expires),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	l, created, err := e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "sorted/a.txt"})
	if err != nil || !created {
		t.Fatalf("CreateSymlink = %v, %v", created, err)
	}
	if l.Kind != api.KindSymlink || l.Title != "Title" || l.Description != "Desc" || !reflect.DeepEqual(l.Tags, []string{"x", "y"}) ||
		l.Disposition != api.Attachment || l.Visibility != api.Public || l.ExpiresAt != nil {
		t.Errorf("copied metadata = %+v", l)
	}
	if l.Size != 5 || l.SHA256 != sha("hello") || l.ContentType != orig.ContentType || l.EffectiveVisibility != api.Public || l.URL == nil {
		t.Errorf("resolved content = %+v", l)
	}
	if l.Target == nil || l.Target.ID != orig.ID || *l.Target.Path != "orig/a.txt" || l.Target.State != api.TargetOK {
		t.Errorf("target = %+v", l.Target)
	}

	// copy_meta=false takes server defaults; explicit fields win over both.
	l, _, err = e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{
		Source: "orig/a.txt", Path: "sorted/b.txt", CopyMeta: ptr(false), Tags: &[]string{"z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.Title != "" || l.Visibility != api.Unlisted || !reflect.DeepEqual(l.Tags, []string{"z"}) || l.Disposition != api.Inline {
		t.Errorf("defaults = %+v", l)
	}
	l, _, err = e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{
		Source: "orig/a.txt", Path: "sorted/c.txt", Title: ptr("Own"), Visibility: ptr(api.Private), ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.Title != "Own" || l.Description != "Desc" || l.Visibility != api.Private || l.URL != nil || l.ExpiresAt == nil {
		t.Errorf("explicit fields = %+v", l)
	}

	// A symlink source is dereferenced; its own metadata is what gets copied.
	_, err = e.svc.Patch(e.ctx, e.admin, "sorted/c.txt", api.ObjectPatch{Title: api.Some("Via link")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	l = e.ln(e.admin, "sorted/c.txt", "sorted/d.txt")
	if l.Target.ID != orig.ID || l.Title != "Via link" || l.Visibility != api.Private {
		t.Errorf("dereferenced = %+v", l)
	}
}

func TestCreateSymlinkEffectiveVisibility(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("orig/u.txt", "u", api.Unlisted)
	l, _, err := e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/u.txt", Path: "ln/u.txt", Visibility: ptr(api.Public)})
	if err != nil {
		t.Fatal(err)
	}
	if l.Visibility != api.Public || l.EffectiveVisibility != api.Unlisted || *l.Target.Visibility != api.Unlisted {
		t.Errorf("symlink = %+v", l)
	}
}

func TestCreateSymlinkErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("orig/a.txt", "a", api.Public)
	e.put("orig/sub/b.txt", "b", api.Public)
	e.put("taken.txt", "t", api.Public)
	e.put("secret/s.txt", "s", api.Private)
	e.ln(e.admin, "orig/a.txt", "orig/self-link")
	e.ln(e.admin, "secret/s.txt", "pub/to-secret")
	e.put("gone.txt", "g", api.Public)
	e.ln(e.admin, "gone.txt", "broken")
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "gone.txt", Purge: true}); err != nil {
		t.Fatal(err)
	}
	rw := e.principal(api.ScopeRW, "pub/")
	for name, tc := range map[string]struct {
		p    service.Principal
		req  api.CreateSymlinkRequest
		want error
	}{
		"read-only key":         {e.principal(api.ScopeRO, ""), api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "x"}, service.ErrForbidden},
		"missing source":        {e.admin, api.CreateSymlinkRequest{Source: "nope.txt", Path: "x"}, service.ErrNotFound},
		"directory with slash":  {e.admin, api.CreateSymlinkRequest{Source: "orig/", Path: "x"}, service.ErrInvalidRequest},
		"directory no slash":    {e.admin, api.CreateSymlinkRequest{Source: "orig/sub", Path: "x"}, service.ErrInvalidRequest},
		"broken source":         {e.admin, api.CreateSymlinkRequest{Source: "broken", Path: "x"}, service.ErrNotFound},
		"taken":                 {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "taken.txt"}, service.ErrAlreadyExists},
		"own target":            {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "orig/a.txt", Overwrite: true}, service.ErrInvalidRequest},
		"own target via link":   {e.admin, api.CreateSymlinkRequest{Source: "orig/self-link", Path: "orig/a.txt", Overwrite: true}, service.ErrInvalidRequest},
		"parent is a file":      {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "taken.txt/x"}, service.ErrPathConflict},
		"path is a directory":   {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "orig/sub"}, service.ErrPathConflict},
		"reserved path":         {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "api/x"}, service.ErrInvalidPath},
		"bad metadata":          {e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "x", Visibility: ptr(api.Visibility("open"))}, service.ErrInvalidRequest},
		"source outside prefix": {rw, api.CreateSymlinkRequest{Source: "secret/s.txt", Path: "pub/x"}, service.ErrForbidden},
		"path outside prefix":   {e.principal(api.ScopeRW, "secret/"), api.CreateSymlinkRequest{Source: "secret/s.txt", Path: "pub/x"}, service.ErrForbidden},
		// The source lies in the prefix, but its target does not: for this
		// key the symlink is broken.
		"target outside prefix": {rw, api.CreateSymlinkRequest{Source: "pub/to-secret", Path: "pub/x"}, service.ErrNotFound},
	} {
		_, _, err := e.svc.CreateSymlink(e.ctx, tc.p, tc.req)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v; want %v", name, err, tc.want)
		}
	}
}

func TestCreateSymlinkOverwrite(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.put("orig/a.txt", "aaa", api.Public)
	b := e.put("orig/b.txt", "bb", api.Public)
	l := e.ln(e.admin, "orig/a.txt", "ln/x")
	_, err := e.svc.Patch(e.ctx, e.admin, "ln/x", api.ObjectPatch{Title: api.Some("Mine")}, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Retargeting keeps the ID (and so signed links) and the metadata.
	r, created, err := e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/b.txt", Path: "ln/x", Overwrite: true})
	if err != nil || created {
		t.Fatalf("retarget = %v, %v", created, err)
	}
	if r.ID != l.ID || r.Target.ID != b.ID || r.Title != "Mine" || r.Revision != l.Revision+2 || r.Size != 2 {
		t.Errorf("retargeted = %+v", r)
	}
	// copy_meta=true copies again on retarget.
	r, _, err = e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "ln/x", Overwrite: true, CopyMeta: ptr(true)})
	if err != nil || r.Title != "" || r.Target.ID != a.ID {
		t.Errorf("retarget with copy = %+v, %v", r, err)
	}

	// Overwriting a file moves it to the trash and creates a new symlink.
	f := e.put("ln/file.txt", "f", api.Public)
	r, created, err = e.svc.CreateSymlink(e.ctx, e.admin, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "ln/file.txt", Overwrite: true})
	if err != nil || !created || r.ID == f.ID {
		t.Fatalf("replace file = %+v, %v, %v", r, created, err)
	}
	trash, err := e.svc.ListTrash(e.ctx, e.admin, service.TrashParams{Path: "ln/file.txt"})
	if err != nil || len(trash.Entries) != 1 || trash.Entries[0].Object.ID != f.ID {
		t.Errorf("trash = %+v, %v", trash, err)
	}
}
