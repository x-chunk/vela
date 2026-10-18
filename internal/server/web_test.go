package server_test

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/server"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/testutil"
)

const sessionCSP = "default-src 'self'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

const anonymousCSP = "default-src 'self'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// webAuth enables web sign-in; insecure selects the development cookie.
func webAuth(insecure bool) testutil.Option {
	return testutil.WithServerOptions(func(o *server.Options) {
		o.WebAuth = true
		o.InsecureCookie = insecure
	})
}

func newWebHarness(t *testing.T, opts ...testutil.Option) *harness {
	t.Helper()
	return newHarness(t, append([]testutil.Option{webAuth(true)}, opts...)...)
}

// signIn posts key to the sign-in form as a same-origin browser would and
// returns the response.
func (h *harness) signIn(key, next string, headers ...string) resp {
	h.t.Helper()
	target := "/_vela/login"
	if next != "" {
		target += "?next=" + url.QueryEscape(next)
	}
	form := url.Values{"key": {key}}.Encode()
	headers = append([]string{"Content-Type", "application/x-www-form-urlencoded", "Sec-Fetch-Site", "same-origin"}, headers...)
	return h.do("POST", target, "", strings.NewReader(form), headers...)
}

// session signs in and returns the cookie header value of the session.
func (h *harness) session(key string) string {
	h.t.Helper()
	r := h.signIn(key, "")
	expectStatus(h.t, r, http.StatusSeeOther)
	c := sessionCookie(h.t, r)
	return c.Name + "=" + c.Value
}

func sessionCookie(t *testing.T, r resp) *http.Cookie {
	t.Helper()
	cookies := (&http.Response{Header: r.header}).Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", r.header.Values("Set-Cookie"))
	}
	return cookies[0]
}

func (h *harness) as(cookie, method, path string, headers ...string) resp {
	h.t.Helper()
	return h.do(method, path, "", nil, append([]string{"Cookie", cookie}, headers...)...)
}

func expectHeaders(t *testing.T, r resp, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got := strings.Join(r.header.Values(k), ", "); got != v {
			t.Errorf("%s = %q; want %q", k, got, v)
		}
	}
}

func TestWebAuthDisabled(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.upload("pub.txt", "p", api.HeaderVisibility, "public")
	expectStatus(t, h.do("GET", "/_vela/login", "", nil), 404)
	expectStatus(t, h.do("POST", "/_vela/logout", "", nil), 405)
	r := h.signIn(h.ts.AdminKey, "")
	expectStatus(t, r, 405)
	if r.header.Get("Set-Cookie") != "" {
		t.Fatal("cookie set while web sign-in is disabled")
	}
	r = h.do("GET", "/", "", nil)
	if bytes.Contains(r.body, []byte("sign in")) || r.header.Get("Vary") != "" || r.header.Get("Referrer-Policy") != "" {
		t.Fatalf("disabled sign-in changed the index: %v\n%s", r.header, r.body)
	}
	for _, p := range []string{"/pub.txt", "/missing"} {
		if v := h.do("GET", p, "", nil).header.Get("Vary"); v != "" {
			t.Fatalf("%s Vary = %q", p, v)
		}
	}
}

func TestSignInAndOut(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	var mu sync.Mutex
	h := newWebHarness(t, testutil.WithLogOutput(lockedWriter{&mu, &logs}))
	h.upload("docs/pub.txt", "p", api.HeaderVisibility, "public")
	h.upload("docs/secret.txt", "s", api.HeaderVisibility, "private")

	// The form: no key in the URL is ever used or echoed.
	r := h.do("GET", "/_vela/login?next=%2Fdocs%2F&key="+h.ts.AdminKey+"&token=t0ken", "", nil)
	expectStatus(t, r, 200)
	expectHeaders(t, r, map[string]string{
		"Cache-Control": "private, no-store", "Content-Security-Policy": sessionCSP, "Referrer-Policy": "same-origin",
		"X-Robots-Tag": "noindex, nofollow", "Vary": "Cookie",
	})
	if !bytes.Contains(r.body, []byte(`action="/_vela/login?next=%2Fdocs%2F"`)) || !bytes.Contains(r.body, []byte(`type="password" name="key" autocomplete="off"`)) {
		t.Fatalf("sign-in form = %s", r.body)
	}
	if bytes.Contains(r.body, []byte(h.ts.AdminKey)) || bytes.Contains(r.body, []byte("t0ken")) {
		t.Fatal("query parameters echoed")
	}

	// Cross-site posts are refused before the key is looked at.
	for _, hdr := range [][]string{
		{"Sec-Fetch-Site", "cross-site"},
		{"Sec-Fetch-Site", "same-site"},
		{"Origin", "https://evil.example"},
		{"Origin", "null"},
		{},
	} {
		form := strings.NewReader(url.Values{"key": {h.ts.AdminKey}}.Encode())
		r := h.do("POST", "/_vela/login", "", form, append([]string{"Content-Type", "application/x-www-form-urlencoded"}, hdr...)...)
		expectStatus(t, r, 403)
		if r.header.Get("Set-Cookie") != "" {
			t.Fatalf("%v: cookie set", hdr)
		}
	}
	// An Origin that matches the public URL is enough for older browsers.
	form := strings.NewReader(url.Values{"key": {h.ts.AdminKey}}.Encode())
	expectStatus(t, h.do("POST", "/_vela/login", "", form, "Content-Type", "application/x-www-form-urlencoded", "Origin", h.ts.URL), 303)

	r = h.signIn("vela_zzzzzzzzzz_"+strings.Repeat("A", 43), "/docs/")
	expectStatus(t, r, 401)
	if !bytes.Contains(r.body, []byte("Invalid API key.")) || r.header.Get("Set-Cookie") != "" {
		t.Fatalf("failed sign-in = %v %s", r.header, r.body)
	}
	// A key in the URL is ignored even on the form's own target.
	r = h.do("POST", "/_vela/login?key="+url.QueryEscape(h.ts.AdminKey), "", strings.NewReader(""),
		"Content-Type", "application/x-www-form-urlencoded", "Sec-Fetch-Site", "same-origin")
	expectStatus(t, r, 401)

	r = h.signIn(h.ts.AdminKey, "/docs/")
	expectStatus(t, r, 303)
	expectHeaders(t, r, map[string]string{"Location": "/docs/", "Cache-Control": "private, no-store"})
	c := sessionCookie(t, r)
	if c.Name != "vela_session" || len(c.Value) != 43 || !c.HttpOnly || c.Secure || c.Path != "/" || c.Domain != "" ||
		c.SameSite != http.SameSiteLaxMode || c.MaxAge != int((30*api.Day).Seconds()) {
		t.Fatalf("cookie = %+v", c)
	}
	cookie := c.Name + "=" + c.Value

	// A signed-in page: never cached by others, forms allowed, private
	// files listed with their indicator, and a sign-out button.
	r = h.as(cookie, "GET", "/docs/")
	expectStatus(t, r, 200)
	expectHeaders(t, r, map[string]string{
		"Cache-Control": "private, no-store", "Content-Security-Policy": sessionCSP, "Vary": "Cookie", "Referrer-Policy": "same-origin",
	})
	for _, want := range []string{`secret.txt</span></a><span class="flag flag-private"`, "signed in: full access",
		`<form class="signout" method="post" action="/_vela/logout?next=%2Fdocs%2F">`} {
		if !bytes.Contains(r.body, []byte(want)) {
			t.Errorf("signed-in page lacks %q", want)
		}
	}
	r = h.as(cookie, "GET", "/docs/secret.txt")
	expectStatus(t, r, 200)
	expectHeaders(t, r, map[string]string{"Cache-Control": "private, no-store", "Vary": "Cookie"})
	if string(r.body) != "s" {
		t.Fatalf("private body = %q", r.body)
	}
	// The same URL is 404 for anonymous visitors, also varying by cookie.
	r = h.do("GET", "/docs/secret.txt", "", nil)
	expectStatus(t, r, 404)
	expectHeaders(t, r, map[string]string{"Cache-Control": "no-cache", "Vary": "Cookie", "Content-Security-Policy": anonymousCSP})
	if !bytes.Contains(r.body, []byte(`<a class="auth" href="/_vela/login?next=%2Fdocs%2Fsecret.txt">sign in</a>`)) {
		t.Fatalf("404 page lacks the sign-in link: %s", r.body)
	}
	r = h.as(cookie, "GET", "/missing")
	expectStatus(t, r, 404)
	expectHeaders(t, r, map[string]string{"Cache-Control": "private, no-store", "Content-Security-Policy": sessionCSP, "Vary": "Cookie"})

	// Sign-out needs a same-origin POST, deletes the row and expires the
	// cookie: the old cookie value stops working, not just this copy.
	expectStatus(t, h.as(cookie, "GET", "/_vela/logout"), 405)
	expectStatus(t, h.as(cookie, "POST", "/_vela/logout", "Origin", "https://evil.example"), 403)
	expectStatus(t, h.as(cookie, "GET", "/docs/secret.txt"), 200)
	r = h.as(cookie, "POST", "/_vela/logout?next=%2Fdocs%2F", "Sec-Fetch-Site", "same-origin")
	expectStatus(t, r, 303)
	if out := sessionCookie(t, r); out.Name != "vela_session" || out.MaxAge >= 0 || r.header.Get("Location") != "/docs/" {
		t.Fatalf("sign-out = %v", r.header)
	}
	expectStatus(t, h.as(cookie, "GET", "/docs/secret.txt"), 404)
	if r := h.as(cookie, "GET", "/docs/"); !bytes.Contains(r.body, []byte("sign in")) || r.header.Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("stale cookie still signed in: %v", r.header)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, secret := range []string{h.ts.AdminKey, "t0ken", c.Value, "key="} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log contains %q", secret)
		}
	}
	if !strings.Contains(logs.String(), "route=login") || !strings.Contains(logs.String(), "key_id=") {
		t.Errorf("sign-in not in the access log:\n%s", logs.String())
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestSecureSessionCookie(t *testing.T) {
	t.Parallel()
	h := newHarness(t, webAuth(false))
	h.upload("secret.txt", "s", api.HeaderVisibility, "private")
	r := h.signIn(h.ts.AdminKey, "")
	expectStatus(t, r, 303)
	c := sessionCookie(t, r)
	if c.Name != "__Host-vela_session" || !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" {
		t.Fatalf("cookie = %+v", c)
	}
	expectStatus(t, h.as(c.Name+"="+c.Value, "GET", "/secret.txt"), 200)
	// The development name is not accepted in secure mode: it lacks the
	// __Host- guarantees.
	expectStatus(t, h.as("vela_session="+c.Value, "GET", "/secret.txt"), 404)
}

func TestSignInNext(t *testing.T) {
	t.Parallel()
	h := newWebHarness(t)
	for next, want := range map[string]string{
		"":                          "/",
		"/docs/":                    "/docs/",
		"/docs/a%20b/?format=json":  "/docs/a%20b/",
		"//evil.example":            "/",
		"https://evil.example":      "/",
		"/\\evil.example":           "/",
		"/\t/evil.example":          "/",
		"javascript:alert(1)":       "/",
		"evil.example":              "/",
		"/_vela/logout":             "/",
		"/%2F%2Fevil.example":       "/%2F%2Fevil.example",
		"/\u3000/x":                 "/%E3%80%80/x",
		"https:/\\/evil.example/x/": "/",
	} {
		r := h.signIn(h.ts.AdminKey, next)
		expectStatus(t, r, 303)
		if got := r.header.Get("Location"); got != want {
			t.Errorf("next %q: Location = %q; want %q", next, got, want)
		}
	}
}

func TestSignInRateLimit(t *testing.T) {
	t.Parallel()
	h := newWebHarness(t)
	for i := 0; i < 5; i++ {
		expectStatus(t, h.signIn("wrong", ""), 401)
	}
	r := h.signIn(h.ts.AdminKey, "")
	expectStatus(t, r, 429)
	if r.header.Get("Retry-After") == "" || !bytes.Contains(r.body, []byte("Too many failed attempts")) || r.header.Get("Set-Cookie") != "" {
		t.Fatalf("throttled sign-in = %v %s", r.header, r.body)
	}
	// Another client is unaffected; the API keeps its own budget.
	expectStatus(t, h.signIn(h.ts.AdminKey, "", "X-Real-IP", "198.51.100.7"), 303)
	expectStatus(t, h.admin("GET", api.RouteInfo, ""), 200)
	h.ts.Clock.Advance(13 * time.Second)
	expectStatus(t, h.signIn(h.ts.AdminKey, ""), 303)
}

func TestSessionFollowsKey(t *testing.T) {
	t.Parallel()
	h := newWebHarness(t, testutil.WithServiceOptions(func(o *service.Options) { o.WebSessionIdle = 2 * time.Hour }))
	h.upload("secret.txt", "s", api.HeaderVisibility, "private")
	key, err := h.ts.Service.CreateKey(t.Context(), "browser", api.ScopeRO, "")
	if err != nil {
		t.Fatal(err)
	}
	cookie := h.session(key.Full)
	expectStatus(t, h.as(cookie, "GET", "/secret.txt"), 200)
	// Signing in again ends the previous session of the browser.
	second := sessionCookie(t, h.signIn(key.Full, "", "Cookie", cookie))
	expectStatus(t, h.as(cookie, "GET", "/secret.txt"), 404)
	cookie = second.Name + "=" + second.Value

	// Idle expiry: activity keeps the session, silence ends it.
	h.ts.Clock.Advance(90 * time.Minute)
	expectStatus(t, h.as(cookie, "GET", "/secret.txt"), 200)
	h.ts.Clock.Advance(90 * time.Minute)
	expectStatus(t, h.as(cookie, "GET", "/secret.txt"), 200)
	h.ts.Clock.Advance(2*time.Hour + time.Second)
	expectStatus(t, h.as(cookie, "GET", "/secret.txt"), 404)

	// Revoking the key makes the very next request anonymous.
	cookie = h.session(key.Full)
	expectStatus(t, h.as(cookie, "GET", "/secret.txt"), 200)
	if err := h.ts.Service.RevokeKey(t.Context(), key.Key.ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, h.as(cookie, "GET", "/secret.txt"), 404)
	expectStatus(t, h.signIn(key.Full, ""), 401)
}

func TestSessionAbsoluteExpiry(t *testing.T) {
	t.Parallel()
	h := newWebHarness(t, testutil.WithServiceOptions(func(o *service.Options) {
		o.WebSessionIdle = 2 * time.Hour
		o.WebSessionMax = 3 * time.Hour
	}))
	h.upload("secret.txt", "s", api.HeaderVisibility, "private")
	r := h.signIn(h.ts.AdminKey, "")
	if c := sessionCookie(t, r); c.MaxAge != 3*3600 {
		t.Fatalf("cookie lifetime = %d", c.MaxAge)
	}
	cookie := h.session(h.ts.AdminKey)
	for range 3 {
		h.ts.Clock.Advance(59 * time.Minute)
		expectStatus(t, h.as(cookie, "GET", "/secret.txt"), 200)
	}
	h.ts.Clock.Advance(4 * time.Minute)
	expectStatus(t, h.as(cookie, "GET", "/secret.txt"), 404)
}

// Acceptance checks 2 and 4 of docs/SPEC-web.md over HTTP.
func TestSignedInPrefixView(t *testing.T) {
	t.Parallel()
	h := newWebHarness(t)
	h.upload("pub.txt", "p", api.HeaderVisibility, "public")
	h.upload("test/a.txt", "a", api.HeaderVisibility, "private")
	h.upload("test/b.html", "<script>1</script>", api.HeaderVisibility, "unlisted", "Content-Type", "text/html")
	h.upload("secret/x.txt", "x", api.HeaderVisibility, "private")
	h.upload("test2/y.txt", "y", api.HeaderVisibility, "private")
	for _, prefix := range []string{"test/", "secret/", "test2/"} {
		h.dirPatch(prefix, `{"hidden":true}`)
	}
	key, err := h.ts.Service.CreateKey(t.Context(), "test", api.ScopeAdmin, "test/")
	if err != nil {
		t.Fatal(err)
	}
	cookie := h.session(key.Full)

	r := h.as(cookie, "GET", "/")
	expectStatus(t, r, 200)
	body := string(r.body)
	if !strings.Contains(body, `<span class="name">test/</span></a><span class="flag flag-hidden"`) || strings.Contains(body, "secret/") ||
		strings.Contains(body, "test2/") || !strings.Contains(body, "signed in: /test/") {
		t.Fatalf("root = %s", body)
	}
	r = h.as(cookie, "GET", "/test/?format=json")
	expectStatus(t, r, 200)
	expectHeaders(t, r, map[string]string{"Cache-Control": "private, no-store", "Vary": "Cookie"})
	var page struct {
		Session struct {
			Prefix string `json:"prefix"`
			Full   bool   `json:"full"`
		} `json:"session"`
		Files []struct {
			Name  string   `json:"name"`
			Flags []string `json:"flags"`
		} `json:"files"`
	}
	r.json(t, &page)
	if page.Session.Prefix != "test/" || page.Session.Full || len(page.Files) != 2 || page.Files[0].Flags[0] != "private" || page.Files[1].Flags[0] != "unlisted" {
		t.Fatalf("JSON = %s", r.body)
	}
	for _, p := range []string{"/secret/", "/secret/x.txt", "/test2/", "/test2/y.txt", "/secret"} {
		r := h.as(cookie, "GET", p)
		expectStatus(t, r, 404)
		expectHeaders(t, r, map[string]string{"Cache-Control": "private, no-store"})
	}
	r = h.as(cookie, "GET", "/test")
	expectStatus(t, r, 301)
	expectHeaders(t, r, map[string]string{"Location": "/test/", "Cache-Control": "private, no-store", "Vary": "Cookie"})
	r = h.do("GET", "/test", "", nil)
	expectStatus(t, r, 404)

	// Anonymous JSON keeps its shape.
	r = h.do("GET", "/?format=json", "", nil)
	if bytes.Contains(r.body, []byte(`"session"`)) || bytes.Contains(r.body, []byte(`"flags"`)) {
		t.Fatalf("anonymous JSON = %s", r.body)
	}
}

// Acceptance check 1: anonymous pages differ only by the footer link and
// Vary: Cookie (plus Referrer-Policy on HTML) when sign-in is enabled.
func TestAnonymousPagesWithSignIn(t *testing.T) {
	h := newWebHarness(t, testutil.WithStartTime(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)))
	for _, p := range []string{"top.txt", "docs/a.txt", "docs/sub/b.txt", "docs/sub/c.txt"} {
		h.upload(p, "example", api.HeaderVisibility, "public")
	}
	h.upload("docs/private.txt", "secret", api.HeaderVisibility, "private")
	for _, tc := range []struct{ name, route, next string }{{"root.html", "/", "%2F"}, {"docs.html", "/docs/", "%2Fdocs%2F"}} {
		r := h.do("GET", tc.route, "", nil)
		expectStatus(t, r, 200)
		expectHeaders(t, r, map[string]string{
			"Vary": "Cookie", "Cache-Control": "public, max-age=60", "Content-Security-Policy": anonymousCSP, "Referrer-Policy": "same-origin",
		})
		want, err := os.ReadFile(filepath.Join("testdata", "index", tc.name))
		if err != nil {
			t.Fatal(err)
		}
		link := "\n  <a class=\"auth\" href=\"/_vela/login?next=" + tc.next + "\">sign in</a>"
		want = bytes.Replace(want, []byte("<span>Served by Vela test</span>"), []byte("<span>Served by Vela test</span>"+link), 1)
		body := bytes.ReplaceAll(r.body, []byte(h.ts.URL), []byte("https://store.example.com"))
		if !bytes.Equal(body, want) {
			t.Fatalf("anonymous %s differs:\n%s", tc.route, body)
		}
	}
}

func TestActiveContentSandbox(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		var opts []testutil.Option
		if enabled {
			opts = append(opts, webAuth(true))
		}
		h := newHarness(t, opts...)
		h.upload("page.html", "<script>fetch('/')</script>", api.HeaderVisibility, "public", "Content-Type", "text/html; charset=utf-8")
		h.upload("pic.svg", "<svg/>", api.HeaderVisibility, "unlisted", "Content-Type", "image/svg+xml")
		h.upload("doc.pdf", "%PDF", api.HeaderVisibility, "public", "Content-Type", "application/pdf")
		h.upload("pic.png", "\x89PNG", api.HeaderVisibility, "public", "Content-Type", "image/png")
		h.upload("push.bin", "--x", api.HeaderVisibility, "public", "Content-Type", "multipart/x-mixed-replace; boundary=x")
		h.upload("private/page.xhtml", "<html/>", api.HeaderVisibility, "private", "Content-Type", "application/xhtml+xml")
		r := h.admin("POST", api.RouteLinks, `{"path":"private/page.xhtml","ttl":"1h"}`)
		expectStatus(t, r, 201)
		var l api.Link
		r.json(t, &l)
		u, err := url.Parse(*l.URL)
		if err != nil {
			t.Fatal(err)
		}
		sandbox := "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads"
		for path, active := range map[string]bool{
			"/page.html": true, "/pic.svg": true, "/push.bin": true, u.RequestURI(): true, "/doc.pdf": false, "/pic.png": false,
		} {
			r := h.do("GET", path, "", nil)
			expectStatus(t, r, 200)
			want := ""
			if enabled && active {
				want = sandbox
			}
			if got := r.header.Get("Content-Security-Policy"); got != want {
				t.Errorf("web auth %t, %s: CSP = %q; want %q", enabled, path, got, want)
			}
		}
	}
}

func TestWebAuthAccelHeaders(t *testing.T) {
	t.Parallel()
	h := newWebHarness(t, testutil.WithAccel("/_blobs/"))
	h.upload("page.html", "<p>", api.HeaderVisibility, "public", "Content-Type", "text/html")
	h.upload("secret.txt", "s", api.HeaderVisibility, "private")
	r := h.do("GET", "/page.html", "", nil)
	expectStatus(t, r, 200)
	// nginx drops these headers from an X-Accel-Redirect response, so they
	// travel as X-Vela-* and the internal location re-emits them.
	expectHeaders(t, r, map[string]string{
		"X-Vela-Vary": "Cookie", "X-Vela-Content-Security-Policy": "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads",
		"X-Vela-Cache-Control": "public, max-age=300", "Vary": "", "Content-Security-Policy": "", "Cache-Control": "",
	})
	cookie := h.session(h.ts.AdminKey)
	r = h.as(cookie, "GET", "/secret.txt")
	expectStatus(t, r, 200)
	expectHeaders(t, r, map[string]string{"X-Vela-Cache-Control": "private, no-store", "X-Vela-Vary": "Cookie", "Cache-Control": ""})
	if !strings.HasPrefix(r.header.Get("X-Accel-Redirect"), "/_blobs/") {
		t.Fatalf("X-Accel-Redirect = %q", r.header.Get("X-Accel-Redirect"))
	}
	// A revalidation is answered by velad itself, with the headers set
	// directly.
	r = h.as(cookie, "GET", "/secret.txt", "If-None-Match", r.header.Get("X-Vela-ETag"))
	expectStatus(t, r, 304)
	expectHeaders(t, r, map[string]string{"Cache-Control": "private, no-store", "Vary": "Cookie"})
}
