package server_test

import (
	"net/http"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/testutil"
)

func (h *harness) symlink(body string) api.Object {
	h.t.Helper()
	r := h.admin("POST", api.RouteSymlinks, body)
	if r.status != http.StatusCreated && r.status != http.StatusOK {
		h.t.Fatalf("symlink %s: %d %s", body, r.status, r.body)
	}
	var o api.Object
	r.json(h.t, &o)
	return o
}

func TestSymlinkAPIContract(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.upload("orig/a.txt", "aaa", api.HeaderVisibility, "public")
	h.upload("orig/b.txt", "bb", api.HeaderVisibility, "private")

	r := h.admin("POST", api.RouteSymlinks, `{"source":"orig/a.txt","path":"ln/a.txt"}`)
	expectStatus(t, r, 201)
	var o api.Object
	r.json(t, &o)
	if r.header.Get("Location") != api.ObjectRoute("ln/a.txt") || r.header.Get("ETag") != `"r1"` {
		t.Errorf("headers = %v", r.header)
	}
	if o.Kind != api.KindSymlink || o.Target == nil || o.Target.ID != a.ID || o.SHA256 != a.SHA256 || o.EffectiveVisibility != api.Public {
		t.Errorf("created = %+v", o)
	}

	for body, want := range map[string]struct {
		status int
		code   api.ErrorCode
	}{
		`{"source":"orig/a.txt","path":"ln/a.txt"}`:              {409, api.CodeAlreadyExists},
		`{"source":"missing","path":"ln/x"}`:                     {404, api.CodeNotFound},
		`{"source":"orig/","path":"ln/x"}`:                       {400, api.CodeInvalidRequest},
		`{"source":"orig/a.txt","path":"orig/a.txt/x"}`:          {409, api.CodePathConflict},
		`{"source":"orig/a.txt","path":"api/x"}`:                 {400, api.CodeInvalidPath},
		`{"source":"orig/a.txt","path":"ln/x","bogus":1}`:        {400, api.CodeInvalidRequest},
		`{"source":"orig/","path":"orig/x/","recursive":true}`:   {400, api.CodeInvalidRequest},
		`{"source":"orig/","path":"ln/","recursive":true}`:       {409, api.CodeAlreadyExists},
		`{"source":"orig/a.txt","path":"ln/x","tags":["", "a"]}`: {400, api.CodeInvalidRequest},
	} {
		r := h.admin("POST", api.RouteSymlinks, body)
		if r.status != want.status || r.errCode(t) != want.code {
			t.Errorf("%s: %d %s; want %d %s", body, r.status, r.body, want.status, want.code)
		}
	}
	r = h.admin("POST", api.RouteSymlinks, `{"source":"orig/","path":"ln/","recursive":true}`)
	var conflict api.ErrorResponse
	r.json(t, &conflict)
	if conflicts, _ := conflict.Error.Details["conflicts"].([]any); len(conflicts) != 1 || conflicts[0] != "ln/a.txt" {
		t.Errorf("details = %+v", conflict.Error.Details)
	}

	// Retargeting in place answers 200 and keeps the ID.
	r = h.admin("POST", api.RouteSymlinks, `{"source":"orig/b.txt","path":"ln/a.txt","overwrite":true}`)
	expectStatus(t, r, 200)
	var re api.Object
	r.json(t, &re)
	if re.ID != o.ID || re.EffectiveVisibility != api.Private || re.URL != nil {
		t.Errorf("retargeted = %+v", re)
	}

	r = h.admin("POST", api.RouteSymlinks, `{"source":"orig/","path":"tree/","recursive":true}`)
	expectStatus(t, r, 200)
	var tree api.SymlinkTreeResponse
	r.json(t, &tree)
	if tree.Created != 2 || tree.Replaced != 0 || tree.Skipped == nil {
		t.Errorf("tree = %+v", tree)
	}

	// Writing content through a symlink is refused.
	r = h.admin("PUT", api.ContentRoute("ln/a.txt"), "x")
	if r.status != 409 || r.errCode(t) != api.CodeIsSymlink {
		t.Errorf("upload onto symlink = %d %s", r.status, r.body)
	}
	r = h.admin("POST", api.RouteObject, `{"path":"ln/a.txt","content":"x"}`)
	if r.status != 409 || r.errCode(t) != api.CodeIsSymlink {
		t.Errorf("create onto symlink = %d %s", r.status, r.body)
	}

	// Listing filters.
	var listing api.Listing
	h.admin("GET", api.RouteObject+"?recursive=true&kind=symlink", "").json(t, &listing)
	if len(listing.Objects) != 3 {
		t.Errorf("kind=symlink: %d objects", len(listing.Objects))
	}
	h.admin("GET", api.RouteObject+"?recursive=true&target=orig/b.txt", "").json(t, &listing)
	if len(listing.Objects) != 2 {
		t.Errorf("target: %d objects", len(listing.Objects))
	}
	expectStatus(t, h.admin("GET", api.RouteObject+"?kind=dir", ""), 400)
	expectStatus(t, h.admin("GET", api.RouteObject+"?broken=maybe", ""), 400)

	// Delete reports the symlinks it broke.
	var del api.DeleteResponse
	h.admin("DELETE", api.ObjectRoute("orig/b.txt"), "").json(t, &del)
	if del.SymlinksAffected != 2 {
		t.Errorf("delete = %+v", del)
	}
	h.admin("GET", api.RouteObject+"?recursive=true&broken=true", "").json(t, &listing)
	if len(listing.Objects) != 2 {
		t.Errorf("broken: %d objects", len(listing.Objects))
	}
	var purge api.PurgeResponse
	h.admin("DELETE", api.RouteTrash, "").json(t, &purge)
	if purge.Purged != 1 || purge.SymlinksAffected != 2 {
		t.Errorf("purge = %+v", purge)
	}

	var info api.Info
	h.admin("GET", api.RouteInfo, "").json(t, &info)
	if !info.Features.Symlinks {
		t.Error("info does not announce symlinks")
	}

	// Read-only keys cannot create symlinks.
	ro := h.ts.Key(t, "ro", api.ScopeRO, "")
	r = h.do("POST", api.RouteSymlinks, ro, strings.NewReader(`{"source":"orig/a.txt","path":"ln/ro"}`))
	expectStatus(t, r, 403)
}

func TestSymlinkDelivery(t *testing.T) {
	t.Parallel()
	for _, accel := range []bool{false, true} {
		var opts []testutil.Option
		if accel {
			opts = append(opts, testutil.WithAccel("/_blobs/"))
		}
		h := newHarness(t, opts...)
		a := h.upload("orig/a.txt", "0123456789", api.HeaderVisibility, "public")
		h.upload("orig/u.txt", "u", api.HeaderVisibility, "unlisted")
		h.upload("orig/p.txt", "p", api.HeaderVisibility, "private")
		h.symlink(`{"source":"orig/a.txt","path":"ln/copy.txt","disposition":"attachment"}`)
		h.symlink(`{"source":"orig/u.txt","path":"ln/u.txt","visibility":"public"}`)
		h.symlink(`{"source":"orig/p.txt","path":"ln/p.txt","visibility":"public"}`)

		r := h.do("GET", "/ln/copy.txt", "", nil)
		expectStatus(t, r, 200)
		for k, want := range map[string]string{
			"ETag":                `"sha256:` + a.SHA256 + `"`,
			"Content-Disposition": `attachment; filename="copy.txt"; filename*=UTF-8''copy.txt`,
		} {
			got := r.header.Get(k)
			if accel && k == "ETag" {
				got = r.header.Get("X-Vela-ETag")
			}
			if got != want {
				t.Errorf("accel %t: %s = %q; want %q", accel, k, got, want)
			}
		}
		if accel {
			if got := r.header.Get("X-Accel-Redirect"); got == "" {
				t.Errorf("no X-Accel-Redirect: %v", r.header)
			}
			if got := r.header.Get("X-Vela-Cache-Control"); got != "public, max-age=300" {
				t.Errorf("accel cache = %q", got)
			}
			continue
		}
		if string(r.body) != "0123456789" || r.header.Get("Cache-Control") != "public, max-age=300" {
			t.Errorf("direct = %q %v", r.body, r.header)
		}
		r = h.do("GET", "/ln/copy.txt", "", nil, "Range", "bytes=2-4")
		if r.status != 206 || string(r.body) != "234" {
			t.Errorf("range = %d %q", r.status, r.body)
		}
		expectStatus(t, h.do("GET", "/ln/copy.txt", "", nil, "If-None-Match", `"sha256:`+a.SHA256+`"`), 304)
		r = h.do("HEAD", "/ln/copy.txt", "", nil)
		if r.status != 200 || r.header.Get("Content-Length") != "10" {
			t.Errorf("HEAD = %d %v", r.status, r.header)
		}
		// The effective visibility sets the cache policy and robots header.
		r = h.do("GET", "/ln/u.txt", "", nil)
		if r.status != 200 || r.header.Get("X-Robots-Tag") != "noindex, nofollow" {
			t.Errorf("unlisted target = %d %v", r.status, r.header)
		}
		// Refusals look exactly like a missing path.
		missing := h.do("GET", "/ln/none.txt", "", nil)
		priv := h.do("GET", "/ln/p.txt", "", nil)
		if priv.status != 404 || string(priv.body) != string(missing.body) || priv.header.Get("Cache-Control") != missing.header.Get("Cache-Control") {
			t.Errorf("private target = %d %v", priv.status, priv.header)
		}
		// The overwritten target changes the validator.
		b := h.upload("orig/a.txt", "new")
		r = h.do("GET", "/ln/copy.txt", "", nil)
		if string(r.body) != "new" || r.header.Get("ETag") != `"sha256:`+b.SHA256+`"` {
			t.Errorf("after overwrite = %q %v", r.body, r.header)
		}
	}
}

func TestSymlinkSandboxFollowsTarget(t *testing.T) {
	t.Parallel()
	h := newHarness(t, webAuth(true))
	h.upload("orig/page.html", "<script></script>", api.HeaderVisibility, "public", "Content-Type", "text/html; charset=utf-8")
	h.upload("orig/pic.png", "\x89PNG", api.HeaderVisibility, "public", "Content-Type", "image/png")
	h.symlink(`{"source":"orig/page.html","path":"ln/innocent.png"}`)
	h.symlink(`{"source":"orig/pic.png","path":"ln/pic.html"}`)
	if got := h.do("GET", "/ln/innocent.png", "", nil).header.Get("Content-Security-Policy"); got == "" {
		t.Error("HTML behind a symlink is served without the sandbox")
	}
	if got := h.do("GET", "/ln/pic.html", "", nil).header.Get("Content-Security-Policy"); got != "" {
		t.Errorf("image behind a symlink is sandboxed: %q", got)
	}
}
