//go:build e2e

package e2e

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
	velaclient "go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/server"
	"go.xchunk.org/vela/internal/testutil"
)

// TestWebSignInThroughNginx checks that web sign-in decisions made in Go
// survive the reference nginx configuration in accel mode: every header
// exactly once, nothing internal leaking, and no credential in any log.
func TestWebSignInThroughNginx(t *testing.T) {
	ts, n, c := setup(t, testutil.WithServerOptions(func(o *server.Options) { o.WebAuth = true }))
	page := "<script>fetch('/priv.txt')</script>"
	if _, _, err := c.Upload(t.Context(), "page.html", strings.NewReader(page), velaclient.UploadOptions{
		Size: int64(len(page)), ContentType: "text/html; charset=utf-8", Visibility: api.Public,
	}); err != nil {
		t.Fatal(err)
	}
	upload(t, c, "pub.txt", "public", api.Public, "")
	upload(t, c, "priv.txt", "private", api.Private, "")
	sandbox := "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads"
	once := func(what string, r response, names ...string) {
		t.Helper()
		for _, name := range names {
			if got := len(r.header.Values(name)); got != 1 {
				t.Errorf("%s: %s appears %d times: %v", what, name, got, r.header.Values(name))
			}
		}
	}

	r := n.get("GET", n.https+"/page.html")
	if r.status != 200 {
		t.Fatal(r.status)
	}
	expectHeaders(t, "anonymous html", r, map[string]string{"Vary": "Cookie", "Content-Security-Policy": sandbox, "Cache-Control": "public, max-age=300"})
	once("anonymous html", r, "Vary", "Content-Security-Policy", "Cache-Control")
	r = n.get("GET", n.https+"/pub.txt")
	expectHeaders(t, "anonymous text", r, map[string]string{"Vary": "Cookie", "Content-Security-Policy": "", "Cache-Control": "public, max-age=300"})
	r = n.get("GET", n.https+"/priv.txt")
	if r.status != 404 {
		t.Fatalf("anonymous private file: %d", r.status)
	}

	// Sign in through the proxy; the key never appears in a URL.
	form := url.Values{"key": {ts.AdminKey}}.Encode()
	r = n.get("POST", n.https+"/_vela/login?next=%2Fpriv.txt", "Content-Type", "application/x-www-form-urlencoded", "Sec-Fetch-Site", "same-origin", form)
	if r.status != 303 || r.header.Get("Location") != "/priv.txt" {
		t.Fatalf("sign-in: %d %v", r.status, r.header)
	}
	cookies := (&http.Response{Header: r.header}).Cookies()
	if len(cookies) != 1 || cookies[0].Name != "__Host-vela_session" || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("session cookie = %v", r.header.Values("Set-Cookie"))
	}
	cookie := cookies[0].Name + "=" + cookies[0].Value

	r = n.get("GET", n.https+"/priv.txt", "Cookie", cookie)
	if r.status != 200 || r.body != "private" {
		t.Fatalf("signed-in private file: %d %q", r.status, r.body)
	}
	expectHeaders(t, "signed-in file", r, map[string]string{"Cache-Control": "private, no-store", "Vary": "Cookie"})
	once("signed-in file", r, "Cache-Control", "Vary", "X-Content-Type-Options", "Strict-Transport-Security")
	r = n.get("GET", n.https+"/", "Cookie", cookie)
	expectHeaders(t, "signed-in index", r, map[string]string{"Cache-Control": "private, no-store", "Vary": "Cookie", "Referrer-Policy": "same-origin"})
	once("signed-in index", r, "Content-Security-Policy", "X-Content-Type-Options", "Strict-Transport-Security")
	if !strings.Contains(r.header.Get("Content-Security-Policy"), "form-action 'self'") || !strings.Contains(r.body, "priv.txt") {
		t.Fatalf("signed-in index: %v", r.header)
	}

	// Keys in URLs are ignored and, like every query, never logged.
	n.get("GET", n.https+"/?token=SECRETtoken")
	n.get("GET", n.https+"/_vela/login?key=SECRETkey")
	r = n.get("POST", n.https+"/_vela/logout?next=%2F", "Cookie", cookie, "Sec-Fetch-Site", "same-origin", "")
	if r.status != 303 {
		t.Fatalf("sign-out: %d", r.status)
	}
	if r := n.get("GET", n.https+"/priv.txt", "Cookie", cookie); r.status != 404 {
		t.Fatalf("private file after sign-out: %d", r.status)
	}
	logs := n.logs()
	for _, secret := range []string{"SECRETtoken", "SECRETkey", ts.AdminKey, cookies[0].Value} {
		if strings.Contains(logs, secret) {
			t.Errorf("a log contains %q", secret)
		}
	}
	if !strings.Contains(logs, "POST /_vela/login") {
		t.Errorf("sign-in not logged, so the test proves nothing:\n%s", logs)
	}
}
