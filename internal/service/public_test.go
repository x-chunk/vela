package service_test

import (
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

func TestResolvePublic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("pub.txt", "p", api.Public)
	e.put("unl.txt", "u", api.Unlisted)
	e.put("priv.txt", "s", api.Private)
	e.put("dir/a.txt", "a", api.Public)
	e.put("hidden/x.txt", "x", api.Unlisted)

	for _, p := range []string{"pub.txt", "unl.txt", "/pub.txt"} {
		r, err := e.svc.ResolvePublic(e.ctx, p)
		if err != nil || r.Content == nil {
			t.Errorf("%s: %+v, %v", p, r, err)
		}
	}
	for _, p := range []string{"priv.txt", "missing", "hidden", "api/x", "a//b", ""} {
		_, err := e.svc.ResolvePublic(e.ctx, p)
		wantErr(t, err, service.ErrNotFound)
	}
	r, err := e.svc.ResolvePublic(e.ctx, "dir")
	if err != nil || r.RedirectDir != "dir/" {
		t.Fatalf("dir redirect = %+v, %v", r, err)
	}

	// Visibility changes take effect immediately.
	if _, err := e.svc.Patch(e.ctx, e.admin, "pub.txt", api.ObjectPatch{Visibility: api.Some(api.Private)}, 0); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.ResolvePublic(e.ctx, "pub.txt")
	wantErr(t, err, service.ErrNotFound)

	// Expiry hides an object from the public side immediately, before the
	// janitor moves it to the trash.
	_, _, err = e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "brief.txt", Body: stringsReader("x"),
		Visibility: ptr(api.Public), ExpiresAt: api.Some(t0.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(2 * time.Hour)
	_, err = e.svc.ResolvePublic(e.ctx, "brief.txt")
	wantErr(t, err, service.ErrNotFound)
}

func TestIndexShowsOnlyPublic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("a.txt", "a", api.Public)
	e.put("u.txt", "u", api.Unlisted)
	e.put("docs/one.pdf", "1", api.Public)
	e.put("docs/deep/two.pdf", "2", api.Public)
	e.put("docs/secret.pdf", "s", api.Private)
	e.put("hidden/x", "x", api.Unlisted)
	_, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{Path: "soon.txt", Body: stringsReader("x"),
		Visibility: ptr(api.Public), ExpiresAt: api.Some(t0.Add(time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(2 * time.Minute)

	root, err := e.svc.Index(e.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Dirs) != 1 || root.Dirs[0].Path != "docs/" || root.Dirs[0].Count != 2 {
		t.Fatalf("root dirs = %+v", root.Dirs)
	}
	if len(root.Files) != 1 || root.Files[0].Path != "a.txt" {
		t.Fatalf("root files = %+v", root.Files)
	}
	docs, err := e.svc.Index(e.ctx, "docs/")
	if err != nil || len(docs.Files) != 1 || len(docs.Dirs) != 1 {
		t.Fatalf("docs index = %+v, %v", docs, err)
	}
	_, err = e.svc.Index(e.ctx, "hidden/")
	wantErr(t, err, service.ErrNotFound)

	empty := newEnv(t)
	if _, err := empty.svc.Index(empty.ctx, ""); err != nil {
		t.Fatalf("empty root index: %v", err)
	}
}
