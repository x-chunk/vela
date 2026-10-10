package server_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/server"
	"go.xchunk.org/vela/internal/testutil"
)

func TestAPIDownloadDirect(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	o := h.upload("private/data.txt", "0123456789", api.HeaderVisibility, "private")
	r := h.admin("GET", api.ContentRoute("private/data.txt"), "")
	expectStatus(t, r, 200)
	if string(r.body) != "0123456789" {
		t.Fatalf("body = %q", r.body)
	}
	for k, want := range map[string]string{
		"Cache-Control":          "private, no-store",
		"X-Robots-Tag":           "noindex, nofollow",
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
		"Content-Type":           "text/plain; charset=utf-8",
		"ETag":                   `"sha256:` + o.SHA256 + `"`,
		"Content-Disposition":    `inline; filename="data.txt"; filename*=UTF-8''data.txt`,
	} {
		if got := r.header.Get(k); got != want {
			t.Errorf("%s = %q; want %q", k, got, want)
		}
	}
	r = h.admin("GET", api.ContentRoute("private/data.txt"), "", "Range", "bytes=2-4")
	expectStatus(t, r, 206)
	if string(r.body) != "234" {
		t.Fatalf("range body = %q", r.body)
	}
	r = h.admin("GET", api.ContentRoute("private/data.txt"), "", "If-None-Match", `"sha256:`+o.SHA256+`"`)
	expectStatus(t, r, 304)
	r = h.admin("HEAD", api.ContentRoute("private/data.txt"), "")
	if r.status != 200 || len(r.body) != 0 || r.header.Get("Content-Length") != "10" {
		t.Fatalf("HEAD = %d %v", r.status, r.header)
	}
}

func TestPublicVisibility(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.upload("pub.txt", "p", api.HeaderVisibility, "public")
	h.upload("unl.txt", "u", api.HeaderVisibility, "unlisted")
	h.upload("priv.txt", "s", api.HeaderVisibility, "private")
	h.upload("dir/inner.txt", "i", api.HeaderVisibility, "public")
	h.upload("hidden/inner.txt", "i", api.HeaderVisibility, "unlisted")

	r := h.do("GET", "/pub.txt", "", nil)
	expectStatus(t, r, 200)
	if r.header.Get("Cache-Control") != "public, max-age=300" || r.header.Get("X-Robots-Tag") != "" || string(r.body) != "p" {
		t.Fatalf("public: %v %q", r.header, r.body)
	}
	r = h.do("GET", "/unl.txt", "", nil)
	expectStatus(t, r, 200)
	if r.header.Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("unlisted: %v", r.header)
	}
	r = h.do("GET", "/priv.txt", "", nil)
	expectStatus(t, r, 404) // not 403: existence is not disclosed
	if !strings.Contains(r.header.Get("Content-Type"), "text/html") {
		t.Fatalf("404 page type: %v", r.header)
	}
	r = h.do("GET", "/dir", "", nil)
	expectStatus(t, r, 301)
	if r.header.Get("Location") != "/dir/" {
		t.Fatalf("redirect = %q", r.header.Get("Location"))
	}
	expectStatus(t, h.do("GET", "/hidden", "", nil), 404)
	expectStatus(t, h.do("GET", "/hidden/", "", nil), 404)
	expectStatus(t, h.do("GET", "/_blobs/aa/bb/x", "", nil), 404)
	r = h.do("POST", "/pub.txt", "", strings.NewReader("x"))
	expectStatus(t, r, 405)
	if r.header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("Allow = %q", r.header.Get("Allow"))
	}

	// Visibility changes apply immediately.
	h.admin("PATCH", api.ObjectRoute("pub.txt"), `{"visibility":"private"}`)
	expectStatus(t, h.do("GET", "/pub.txt", "", nil), 404)
}

func TestIndexPages(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.upload("pub.txt", "p", api.HeaderVisibility, "public")
	h.upload("unl.txt", "u", api.HeaderVisibility, "unlisted")
	h.upload("docs/a.pdf", "a", api.HeaderVisibility, "public", api.HeaderTitle, "Doc%20A")
	h.upload("docs/secret.pdf", "s", api.HeaderVisibility, "private")

	r := h.do("GET", "/", "", nil)
	expectStatus(t, r, 200)
	page := string(r.body)
	if !strings.Contains(page, "pub.txt") || !strings.Contains(page, "docs/") || strings.Contains(page, "unl.txt") {
		t.Fatalf("root index content wrong:\n%s", page)
	}
	for k, want := range map[string]string{
		"Cache-Control":           "public, max-age=60",
		"Content-Type":            "text/html; charset=utf-8",
		"Content-Security-Policy": "default-src 'self'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
	} {
		if r.header.Get(k) != want {
			t.Errorf("%s = %q", k, r.header.Get(k))
		}
	}
	r = h.do("GET", "/docs/", "", nil)
	expectStatus(t, r, 200)
	if !strings.Contains(string(r.body), "Doc A") || strings.Contains(string(r.body), "secret") {
		t.Fatalf("docs index:\n%s", r.body)
	}
	r = h.do("GET", "/docs/?format=json", "", nil)
	expectStatus(t, r, 200)
	var data struct {
		Path  string `json:"path"`
		Files []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"files"`
	}
	r.json(t, &data)
	if data.Path != "/docs/" || len(data.Files) != 1 || data.Files[0].URL != "/docs/a.pdf" {
		t.Fatalf("json index = %+v", data)
	}
	expectStatus(t, h.do("GET", "/nothing/", "", nil), 404)

	// Theme assets: hashed URLs are immutable.
	css := strings.SplitN(strings.SplitN(page, `href="/_vela/assets/style.css?v=`, 2)[1], `"`, 2)[0]
	r = h.do("GET", "/_vela/assets/style.css?v="+css, "", nil)
	expectStatus(t, r, 200)
	if r.header.Get("Cache-Control") != "public, max-age=31536000, immutable" || r.header.Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("asset headers = %v", r.header)
	}
	expectStatus(t, h.do("GET", "/_vela/assets/nope.css", "", nil), 404)
	r = h.do("GET", "/favicon.ico", "", nil)
	expectStatus(t, r, 200)
	if r.header.Get("Content-Type") != "image/x-icon" {
		t.Fatalf("favicon type = %q", r.header.Get("Content-Type"))
	}
}

func TestRobots(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	r := h.do("GET", "/robots.txt", "", nil)
	if string(r.body) != "User-agent: *\nDisallow: /\n" {
		t.Fatalf("default robots = %q", r.body)
	}
	allow := newHarness(t, testutil.WithServerOptions(func(o *server.Options) { o.RobotsAllow = true }))
	r = allow.do("GET", "/robots.txt", "", nil)
	if !strings.Contains(string(r.body), "Allow: /\nDisallow: /s/\nDisallow: /api/") {
		t.Fatalf("allow robots = %q", r.body)
	}
}

func TestSignedLinkDelivery(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.upload("private/report.pdf", "%PDF-secret", api.HeaderVisibility, "private")
	r := h.admin("POST", api.RouteLinks, `{"path":"private/report.pdf","ttl":"2h","note":"teacher"}`)
	expectStatus(t, r, 201)
	var l api.Link
	r.json(t, &l)
	u, err := url.Parse(*l.URL)
	if err != nil {
		t.Fatal(err)
	}
	r = h.do("GET", u.RequestURI(), "", nil)
	expectStatus(t, r, 200)
	if string(r.body) != "%PDF-secret" {
		t.Fatalf("body = %q", r.body)
	}
	for k, want := range map[string]string{
		"Cache-Control":   "private, no-store",
		"Referrer-Policy": "no-referrer",
		"X-Robots-Tag":    "noindex, nofollow",
	} {
		if r.header.Get(k) != want {
			t.Errorf("%s = %q; want %q", k, r.header.Get(k), want)
		}
	}
	// The filename segment is cosmetic.
	expectStatus(t, h.do("GET", strings.Replace(u.RequestURI(), "report.pdf", "other-name.bin", 1), "", nil), 200)

	exp, _ := strconv.ParseInt(u.Query().Get("exp"), 10, 64)
	q := u.Query()
	q.Set("exp", strconv.FormatInt(exp+3600, 10))
	expectStatus(t, h.do("GET", u.Path+"?"+q.Encode(), "", nil), 404)

	r = h.admin("GET", api.RouteLinks, "")
	var list api.LinkList
	r.json(t, &list)
	if len(list.Links) != 1 || list.Links[0].Note != "teacher" || *list.Links[0].URL != *l.URL {
		t.Fatalf("links = %+v", list)
	}

	h.ts.Clock.Advance(2*time.Hour + time.Second)
	r = h.do("GET", u.RequestURI(), "", nil)
	expectStatus(t, r, 410)
	if r.header.Get("Cache-Control") != "private, no-store" || !strings.Contains(string(r.body), "expired") {
		t.Fatalf("410 page: %v %s", r.header, r.body)
	}

	r = h.admin("POST", api.RouteLinks, `{"path":"private/report.pdf"}`)
	r.json(t, &l)
	expectStatus(t, h.admin("DELETE", api.RouteLinks+"/"+l.ID, ""), 204)
	u, _ = url.Parse(*l.URL)
	expectStatus(t, h.do("GET", u.RequestURI(), "", nil), 404)
	expectStatus(t, h.admin("POST", api.RouteLinks, `{"path":"private/report.pdf","ttl":"60d"}`), 400)
}

func TestAccelDelivery(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testutil.WithAccel("/_blobs/"))
	h.upload("pub.txt", "p", api.HeaderVisibility, "unlisted")
	r := h.do("GET", "/pub.txt", "", nil)
	expectStatus(t, r, 200)
	if len(r.body) != 0 {
		t.Fatalf("accel response has a body: %q", r.body)
	}
	accel := r.header.Get("X-Accel-Redirect")
	if !strings.HasPrefix(accel, "/_blobs/") || len(accel) != len("/_blobs/aa/bb/")+26 {
		t.Fatalf("X-Accel-Redirect = %q", accel)
	}
	for k, want := range map[string]string{
		"X-Vela-Cache-Control":   "public, max-age=300",
		"X-Vela-Robots":          "noindex, nofollow",
		"X-Vela-Referrer-Policy": "",
		"Cache-Control":          "", // nginx would duplicate it; see deliver.go
		"Content-Type":           "text/plain; charset=utf-8",
		"Content-Disposition":    `inline; filename="pub.txt"; filename*=UTF-8''pub.txt`,
	} {
		if got := r.header.Get(k); got != want {
			t.Errorf("%s = %q; want %q", k, got, want)
		}
	}
	r = h.admin("GET", api.ContentRoute("pub.txt"), "")
	if r.header.Get("X-Vela-Referrer-Policy") != "no-referrer" || r.header.Get("X-Vela-Cache-Control") != "private, no-store" {
		t.Fatalf("API download accel headers = %v", r.header)
	}
	if !strings.HasPrefix(r.header.Get("X-Accel-Redirect"), "/_blobs/") || strings.HasPrefix(r.header.Get("X-Accel-Redirect"), "/_blobs/s/") {
		t.Fatalf("API download accel path = %q", r.header.Get("X-Accel-Redirect"))
	}
	// Signed links use their own internal location (no nginx error log).
	r = h.admin("POST", api.RouteLinks, `{"path":"pub.txt"}`)
	var l api.Link
	r.json(t, &l)
	u, _ := url.Parse(*l.URL)
	r = h.do("GET", u.RequestURI(), "", nil)
	if !strings.HasPrefix(r.header.Get("X-Accel-Redirect"), "/_blobs/s/") || len(r.header.Get("X-Accel-Redirect")) != len("/_blobs/s/aa/bb/")+26 {
		t.Fatalf("signed link accel path = %q", r.header.Get("X-Accel-Redirect"))
	}
}

func TestShutdownDraining(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.ts.Server.SetDraining()
	r := h.admin("GET", api.RouteInfo, "")
	expectStatus(t, r, http.StatusServiceUnavailable)
	if r.errCode(t) != api.CodeUnavailable {
		t.Fatal(string(r.body))
	}
}
