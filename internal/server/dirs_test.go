package server_test

import (
	"bytes"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/index"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/testutil"
)

func (h *harness) dirPatch(prefix, body string) api.EffectiveDir {
	h.t.Helper()
	r := h.admin("PATCH", api.DirRoute(prefix), body)
	expectStatus(h.t, r, 200)
	var dir api.EffectiveDir
	r.json(h.t, &dir)
	return dir
}

func TestDirectoryAPI(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	r := h.admin("GET", api.DirRoute("unused/"), "")
	expectStatus(t, r, 200)
	var dir api.EffectiveDir
	r.json(t, &dir)
	if dir.Rule != nil || dir.Exists || dir.URL != nil || dir.Effective.IndexSource != "config" {
		t.Fatal(dir)
	}
	r = h.admin("PUT", api.RouteDirs+"/unused", `{"title":"Prepared","index":"on"}`, "If-None-Match", "*")
	expectStatus(t, r, 201)
	if r.header.Get("ETag") != `"r1"` || r.header.Get("Location") != api.DirRoute("unused/") {
		t.Fatal(r.header)
	}
	h.upload("unused/file", "x", api.HeaderVisibility, "public")
	r = h.admin("GET", api.DirRoute("unused/"), "")
	r.json(t, &dir)
	if !dir.Exists || dir.URL == nil || !dir.ListedInParent {
		t.Fatal(dir)
	}
	expectStatus(t, h.admin("PUT", api.DirRoute("unused/"), `{}`, "If-None-Match", "*"), 412)
	expectStatus(t, h.admin("PATCH", api.DirRoute("unused/"), `{"hidden":true}`, "If-Match", `"r9"`), 412)
	expectStatus(t, h.admin("PATCH", api.DirRoute("missing/"), `{}`, "If-Match", `"r1"`), 412)
	expectStatus(t, h.admin("PUT", api.DirRoute("missing/"), `{}`, "If-Match", `"r1"`), 412)
	r = h.admin("PATCH", api.DirRoute("unused/"), `{"hidden":true,"title":null,"description":"<script>plain</script>"}`, "If-Match", `"r1"`)
	expectStatus(t, r, 200)
	r.json(t, &dir)
	if dir.Rule.Title != "" || !dir.Rule.Hidden || dir.Rule.Revision != 2 || dir.ListedInParent {
		t.Fatal(dir)
	}
	r = h.admin("PUT", api.DirRoute("unused/"), `{}`, "If-Match", `"r2"`)
	expectStatus(t, r, 200)
	r.json(t, &dir)
	if dir.Rule.Hidden || dir.Rule.Description != "" || dir.Rule.Index != api.IndexInherit || dir.Rule.Revision != 3 {
		t.Fatal(dir)
	}
	for _, body := range []string{`null`, `[]`, `{"unknown":1}`, `{"prefix":"a/"}`, `{"hidden":"true"}`, `{"index":"invalid"}`, `{"index":""}`, `{"noindex":""}`, `{"noindex":"maybe"}`, `{"revision":1}`, `{"title":"` + strings.Repeat("x", 257) + `"}`, `{"description":"` + strings.Repeat("x", 4097) + `"}`} {
		for _, method := range []string{"PUT", "PATCH"} {
			r := h.admin(method, api.DirRoute("unused/"), body)
			if r.status != 400 || r.errCode(t) != api.CodeInvalidRequest {
				t.Errorf("%s %s: %d %s", method, body, r.status, r.body)
			}
		}
	}
	expectStatus(t, h.admin("PUT", api.DirRoute("unused/"), `{"hidden":null}`), 400)
	expectStatus(t, h.admin("PUT", api.DirRoute("unused/"), `{}`, "If-None-Match", `"r3"`), 400)
	expectStatus(t, h.admin("PATCH", api.DirRoute(""), `{"hidden":true}`), 400)
	expectStatus(t, h.admin("DELETE", api.DirRoute("unused/"), "", "If-Match", `"r1"`), 412)
	expectStatus(t, h.admin("DELETE", api.DirRoute("unused/"), "", "If-Match", `"r3"`), 204)
	expectStatus(t, h.admin("DELETE", api.DirRoute("unused/"), ""), 404)
	expectStatus(t, h.do("GET", "/unused/file", "", nil), 200)
	for _, prefix := range []string{"", "a/", "a/sub/", "b/"} {
		h.dirPatch(prefix, `{}`)
	}
	var list api.DirPolicyList
	r = h.admin("GET", api.RouteDirs+"?limit=1", "")
	r.json(t, &list)
	if len(list.Policies) != 1 || list.Policies[0].Prefix != "" || list.NextCursor == "" {
		t.Fatal(list)
	}
	r = h.admin("GET", api.RouteDirs+"?limit=1&cursor="+list.NextCursor, "")
	r.json(t, &list)
	if len(list.Policies) != 1 || list.Policies[0].Prefix != "a/" {
		t.Fatal(list)
	}
	r = h.admin("GET", api.RouteDirs+"?prefix=a/&recursive=false", "")
	r.json(t, &list)
	if len(list.Policies) != 2 {
		t.Fatal(list)
	}
	r = h.admin("GET", api.RouteDirs+"?orphaned=true", "")
	r.json(t, &list)
	if len(list.Policies) != 3 {
		t.Fatal(list)
	} // root has a live public descendant
	for _, q := range []string{"?recursive=maybe", "?orphaned=bad", "?limit=-1", "?limit=1001", "?limit=x", "?cursor=!!!"} {
		expectStatus(t, h.admin("GET", api.RouteDirs+q, ""), 400)
	}
}

func TestDirectoryAPIScopes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.dirPatch("", `{"index":"off"}`)
	h.dirPatch("ci/", `{"index":"on"}`)
	h.dirPatch("else/", `{}`)
	ro := h.ts.Key(t, "reader", api.ScopeRO, "ci/")
	rw := h.ts.Key(t, "writer", api.ScopeRW, "ci/")
	unrestrictedRW := h.ts.Key(t, "writer-all", api.ScopeRW, "")
	scopedAdmin := h.ts.Key(t, "admin-ci", api.ScopeAdmin, "ci/")
	r := h.do("GET", api.RouteDirs, ro, nil)
	expectStatus(t, r, 200)
	var list api.DirPolicyList
	r.json(t, &list)
	if len(list.Policies) != 1 || list.Policies[0].Prefix != "ci/" {
		t.Fatal(list)
	}
	r = h.do("GET", api.DirRoute("ci/sub/"), ro, nil)
	expectStatus(t, r, 200)
	var dir api.EffectiveDir
	r.json(t, &dir)
	if dir.Effective.IndexSource != "ci/" {
		t.Fatal(dir)
	}
	// The source may name an ancestor outside the key's own prefix.
	h.dirPatch("ci/", `{"index":null}`)
	r = h.do("GET", api.DirRoute("ci/"), ro, nil)
	r.json(t, &dir)
	if dir.Effective.IndexSource != "" {
		t.Fatal(dir)
	}
	for _, key := range []string{ro, rw, scopedAdmin} {
		expectStatus(t, h.do("GET", api.DirRoute("else/"), key, nil), 403)
		expectStatus(t, h.do("PATCH", api.DirRoute(""), key, strings.NewReader(`{}`)), 403)
	}
	expectStatus(t, h.do("PATCH", api.DirRoute("ci/"), ro, strings.NewReader(`{}`)), 403)
	expectStatus(t, h.do("PATCH", api.DirRoute("ci/"), rw, strings.NewReader(`{}`)), 200)
	expectStatus(t, h.do("PATCH", api.DirRoute(""), unrestrictedRW, strings.NewReader(`{}`)), 403)
	expectStatus(t, h.do("GET", api.RouteDirs+"?prefix=else/", rw, nil), 403)
}

func TestDirectoryPublicPolicies(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testutil.WithStartTime(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)))
	for _, p := range []string{"test0/a.txt", "test0/sub/b.txt", "test0/raw/c.txt", "other/a.txt", "empty/secret/a.txt"} {
		h.upload(p, "public", api.HeaderVisibility, "public")
	}
	h.dirPatch("test0/", `{"index":"on","hidden":true,"title":"Test <area>","description":"Plain <script>description</script>"}`)
	h.dirPatch("test0/raw/", `{"index":"off"}`)
	h.dirPatch("empty/secret/", `{"hidden":true}`)
	root := h.do("GET", "/", "", nil)
	if strings.Contains(string(root.body), "test0") || strings.Contains(string(root.body), "empty/") || !strings.Contains(string(root.body), "other/") {
		t.Fatalf("root leaks hidden directories: %s", root.body)
	}
	for _, route := range []string{"/", "/?format=json"} {
		r := h.do("GET", route, "", nil)
		if strings.Contains(string(r.body), "test0") || strings.Contains(string(r.body), "empty/") {
			t.Fatal(string(r.body))
		}
	}
	h.dirPatch("", `{"index":"off"}`)
	for _, route := range []string{"/", "/?format=json", "/other/", "/other", "/test0/raw/", "/test0/raw", "/missing/", "/missing"} {
		expectStatus(t, h.do("GET", route, "", nil), 404)
	}
	// Disabled and absent directory responses have identical bodies and headers.
	off, absent := h.do("GET", "/other/", "", nil), h.do("GET", "/missing/", "", nil)
	for _, key := range []string{"Date", "X-Request-ID"} {
		off.header.Del(key)
		absent.header.Del(key)
	}
	if !bytes.Equal(off.body, absent.body) || !reflect.DeepEqual(off.header, absent.header) {
		t.Fatalf("distinguishable 404: %v / %v", off.header, absent.header)
	}
	r := h.do("GET", "/test0", "", nil)
	expectStatus(t, r, 301)
	if r.header.Get("Location") != "/test0/" {
		t.Fatal(r.header)
	}
	r = h.do("GET", "/test0/", "", nil)
	expectStatus(t, r, 200)
	body := string(r.body)
	for _, want := range []string{`Test &lt;area&gt;`, `Plain &lt;script&gt;description&lt;/script&gt;`, `href="/test0/sub/"`, `<meta name="robots" content="noindex, nofollow">`, `<span>test store</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s: %s", want, body)
		}
	}
	for _, unwanted := range []string{`href="/"`, `href="/test0/raw/"`, `<script>`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("unexpected %s", unwanted)
		}
	}
	if r.header.Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatal(r.header)
	}
	r = h.do("GET", "/test0/?format=json", "", nil)
	var page index.Page
	r.json(t, &page)
	if page.Parent != "" || page.Breadcrumbs[0].URL != "" || len(page.Dirs) != 1 || page.Dirs[0].Name != "sub" || !page.NoIndex {
		t.Fatal(page)
	}
	r = h.do("GET", "/test0/sub/", "", nil)
	if !strings.Contains(string(r.body), `href="/test0/"`) {
		t.Fatal("hidden ancestor link lost")
	}
	for _, route := range []string{"/test0/a.txt", "/test0/sub/b.txt", "/test0/raw/c.txt"} {
		for _, method := range []string{"GET", "HEAD"} {
			r := h.do(method, route, "", nil)
			expectStatus(t, r, 200)
			if r.header.Get("X-Robots-Tag") != "noindex, nofollow" || r.header.Get("Cache-Control") != "public, max-age=300" {
				t.Fatal(r.header)
			}
		}
	}
	h.dirPatch("test0/sub/", `{"noindex":"off"}`)
	for _, route := range []string{"/test0/sub/", "/test0/sub/b.txt", "/other/a.txt"} {
		r := h.do("GET", route, "", nil)
		expectStatus(t, r, 200)
		if r.header.Get("X-Robots-Tag") != "" || strings.Contains(string(r.body), `name="robots"`) {
			t.Fatal(r.header)
		}
	}
	r = h.do("GET", "/robots.txt", "", nil)
	if strings.Contains(string(r.body), "test0") || strings.Contains(string(r.body), "secret") {
		t.Fatal(string(r.body))
	}
	// Policies do not change authenticated listings or private file access.
	expectStatus(t, h.admin("GET", api.RouteObject+"?recursive=true", ""), 200)
	h.upload("test0/private", "secret", api.HeaderVisibility, "private")
	expectStatus(t, h.do("GET", "/test0/private", "", nil), 404)
}

func TestDirectoryVisibleCountsAndDates(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testutil.WithStartTime(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)))
	for _, p := range []string{"visible/a", "visible/sub/b", "visible/sub/c"} {
		h.upload(p, "x", api.HeaderVisibility, "public")
	}
	visibleTime := h.ts.Clock.Now()
	h.ts.Clock.Advance(time.Hour)
	for _, p := range []string{"visible/hidden/recent", "visible/raw/recent", "empty/hidden/recent"} {
		h.upload(p, "x", api.HeaderVisibility, "public")
	}
	h.dirPatch("visible/hidden/", `{"hidden":true}`)
	h.dirPatch("visible/raw/", `{"index":"off"}`)
	h.dirPatch("empty/hidden/", `{"hidden":true}`)
	r := h.do("GET", "/?format=json", "", nil)
	var page index.Page
	r.json(t, &page)
	if len(page.Dirs) != 1 || page.Dirs[0].Name != "visible" || page.Dirs[0].Count != 2 || !page.Dirs[0].UpdatedAt.Equal(visibleTime) {
		t.Fatalf("leaky counts/dates: %+v", page.Dirs)
	}
	// Empty visible pages remain directly reachable when public descendants exist.
	expectStatus(t, h.do("GET", "/empty/", "", nil), 200)
	h.dirPatch("prepared/", `{"index":"on"}`)
	expectStatus(t, h.do("GET", "/prepared/", "", nil), 404)
}

func TestDirectoryRootConfig(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testutil.WithServiceOptions(func(o *service.Options) { o.RootIndex = api.IndexOff }))
	expectStatus(t, h.do("GET", "/", "", nil), 404)
	h.dirPatch("", `{"index":"on"}`)
	expectStatus(t, h.do("GET", "/", "", nil), 200)
	expectStatus(t, h.admin("DELETE", api.DirRoute(""), ""), 204)
	expectStatus(t, h.do("GET", "/", "", nil), 404)
}

func TestDirectoryRobotsAccel(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testutil.WithAccel("/_blobs/"))
	o := h.upload("hidden/file", "x", api.HeaderVisibility, "public")
	h.dirPatch("hidden/", `{"hidden":true}`)
	r := h.do("GET", "/hidden/file", "", nil)
	expectStatus(t, r, http.StatusOK)
	if r.header.Get("X-Vela-Robots") != "noindex, nofollow" || r.header.Get("X-Vela-Cache-Control") != "public, max-age=300" || r.header.Get("Cache-Control") != "" {
		t.Fatal(r.header)
	}
	r = h.do("GET", "/hidden/file", "", nil, "If-None-Match", `"sha256:`+o.SHA256+`"`)
	expectStatus(t, r, http.StatusNotModified)
	if r.header.Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatal(r.header)
	}
}
