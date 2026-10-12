//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	velaclient "go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/testutil"
)

func setup(t *testing.T) (*testutil.Server, *nginx, *velaclient.Client) {
	t.Helper()
	ts := testutil.NewServer(t, testutil.WithAccel("/_blobs/"))
	n := startNginx(t, strings.TrimPrefix(ts.URL, "http://"), filepath.Join(ts.DataDir, "blobs"))
	c, err := velaclient.New(n.https, ts.AdminKey, velaclient.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	return ts, n, c
}

func upload(t *testing.T, c *velaclient.Client, path, content string, vis api.Visibility, d api.Disposition) *api.Object {
	t.Helper()
	o, _, err := c.Upload(context.Background(), path, strings.NewReader(content), velaclient.UploadOptions{Size: int64(len(content)), Visibility: vis, Disposition: d})
	if err != nil {
		t.Fatalf("upload %s through nginx: %v", path, err)
	}
	return o
}

func expectHeaders(t *testing.T, what string, r response, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got := strings.Join(r.header.Values(k), ", "); got != v {
			t.Errorf("%s: %s = %q; want %q", what, k, got, v)
		}
	}
	// Internal headers must never leak to the client.
	for _, k := range []string{"X-Accel-Redirect", "X-Vela-Cache-Control", "X-Vela-Robots", "X-Vela-Referrer-Policy", "X-Vela-ETag"} {
		if v := r.header.Get(k); v != "" {
			t.Errorf("%s: internal header %s leaked: %q", what, k, v)
		}
	}
}

func TestDeliveryThroughNginx(t *testing.T) {
	ts, n, c := setup(t)
	ctx := context.Background()
	upload(t, c, "docs/Контрольная Q1.pdf", "%PDF-1.4 public content", api.Public, api.Attachment)
	upload(t, c, "docs/unlisted.txt", "unlisted content", api.Unlisted, "")
	upload(t, c, "docs/private.txt", "private content", api.Private, "")
	big := strings.Repeat("0123456789", 300_000)
	upload(t, c, "big.bin", big, api.Public, "")

	t.Run("public", func(t *testing.T) {
		r := n.get("GET", n.https+"/docs/"+url.PathEscape("Контрольная Q1.pdf"))
		if r.status != 200 || r.body != "%PDF-1.4 public content" {
			t.Fatalf("status %d body %q", r.status, r.body)
		}
		expectHeaders(t, "public", r, map[string]string{
			"Content-Type":           "application/pdf",
			"Content-Disposition":    `attachment; filename="___________ Q1.pdf"; filename*=UTF-8''%D0%9A%D0%BE%D0%BD%D1%82%D1%80%D0%BE%D0%BB%D1%8C%D0%BD%D0%B0%D1%8F%20Q1.pdf`,
			"Cache-Control":          "public, max-age=300",
			"X-Robots-Tag":           "",
			"Referrer-Policy":        "",
			"X-Content-Type-Options": "nosniff",
		})
		if r.header.Get("X-Request-ID") == "" {
			t.Errorf("X-Request-ID missing: %v", r.header)
		}
	})

	t.Run("range and head", func(t *testing.T) {
		r := n.get("GET", n.https+"/big.bin", "Range", "bytes=10-19")
		if r.status != 206 || r.body != "0123456789" || r.header.Get("Content-Range") != fmt.Sprintf("bytes 10-19/%d", len(big)) {
			t.Fatalf("range: status %d body %q headers %v", r.status, r.body, r.header)
		}
		r = n.get("HEAD", n.https+"/big.bin")
		if r.status != 200 || r.body != "" || r.header.Get("Content-Length") != fmt.Sprint(len(big)) {
			t.Fatalf("head: status %d length %q", r.status, r.header.Get("Content-Length"))
		}
		if r = n.get("GET", n.https+"/big.bin"); len(r.body) != len(big) {
			t.Fatalf("full body length = %d", len(r.body))
		}
	})

	t.Run("unlisted", func(t *testing.T) {
		r := n.get("GET", n.https+"/docs/unlisted.txt")
		if r.status != 200 || r.body != "unlisted content" {
			t.Fatalf("status %d body %q", r.status, r.body)
		}
		expectHeaders(t, "unlisted", r, map[string]string{
			"Content-Type":  "text/plain; charset=utf-8",
			"Cache-Control": "public, max-age=300",
			"X-Robots-Tag":  "noindex, nofollow",
		})
	})

	t.Run("private and internal", func(t *testing.T) {
		if r := n.get("GET", n.https+"/docs/private.txt"); r.status != 404 {
			t.Fatalf("private by direct URL: %d", r.status)
		}
		for _, p := range []string{"/_blobs/", "/_blobs/aa/bb/" + strings.Repeat("0", 26), "/_blobs/s/aa/bb/" + strings.Repeat("0", 26)} {
			if r := n.get("GET", n.https+p); r.status != 404 {
				t.Fatalf("%s reachable from outside: %d", p, r.status)
			}
		}
	})

	t.Run("signed link", func(t *testing.T) {
		l, err := c.CreateLink(ctx, api.CreateLinkRequest{Path: "docs/private.txt", TTL: "1h"})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(*l.URL)
		r := n.get("GET", n.https+u.RequestURI())
		if r.status != 200 || r.body != "private content" {
			t.Fatalf("status %d body %q", r.status, r.body)
		}
		expectHeaders(t, "signed link", r, map[string]string{
			"Cache-Control":   "private, no-store",
			"X-Robots-Tag":    "noindex, nofollow",
			"Referrer-Policy": "no-referrer",
			"Content-Type":    "text/plain; charset=utf-8",
		})
	})

	t.Run("api download", func(t *testing.T) {
		r := n.get("GET", n.https+api.ContentRoute("docs/private.txt"), "Authorization", "Bearer "+ts.AdminKey)
		if r.status != 200 || r.body != "private content" {
			t.Fatalf("status %d body %q", r.status, r.body)
		}
		expectHeaders(t, "api download", r, map[string]string{
			"Cache-Control":   "private, no-store",
			"Referrer-Policy": "no-referrer",
		})
	})

	t.Run("index", func(t *testing.T) {
		r := n.get("GET", n.https+"/docs/")
		if r.status != 200 || !strings.Contains(r.body, "Контрольная Q1.pdf") || strings.Contains(r.body, "unlisted.txt") {
			t.Fatalf("index: %d", r.status)
		}
		if r.header.Get("Content-Security-Policy") == "" {
			t.Error("index without CSP")
		}
	})

	t.Run("http redirects to https", func(t *testing.T) {
		r := n.get("GET", n.http+"/docs/unlisted.txt")
		if r.status != 301 || !strings.HasPrefix(r.header.Get("Location"), "https://") {
			t.Fatalf("http: %d %v", r.status, r.header)
		}
	})
}

// TestNoSecretsInLogs covers R-007: no log nginx writes contains the query
// of a signed link, neither the access logs (HTTP redirect server included)
// nor the error logs (velad down, blob missing).
func TestNoSecretsInLogs(t *testing.T) {
	ts, n, c := setup(t)
	ctx := context.Background()
	upload(t, c, "a.txt", "a", api.Private, "")
	upload(t, c, "b.txt", "b", api.Private, "")
	good, err := c.CreateLink(ctx, api.CreateLinkRequest{Path: "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	broken, err := c.CreateLink(ctx, api.CreateLinkRequest{Path: "b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	var secrets []string
	query := func(rawURL string) string {
		u, _ := url.Parse(rawURL)
		secrets = append(secrets, u.Query().Get("sig"))
		return u.RequestURI()
	}

	// Plain HTTP: answered by the redirect server.
	if r := n.get("GET", n.http+"/s/01jaaaaaaaaaaaaaaaaaaaaaaa/x.pdf?exp=99999999999&sig=SECRETviaHTTP"); r.status != 301 {
		t.Fatalf("http link: %d", r.status)
	}
	secrets = append(secrets, "SECRETviaHTTP")
	// A working link and a forged one.
	if r := n.get("GET", n.https+query(*good.URL)); r.status != 200 {
		t.Fatalf("good link: %d", r.status)
	}
	if r := n.get("GET", n.https+"/s/01jaaaaaaaaaaaaaaaaaaaaaaa/x.pdf?exp=99999999999&sig=SECRETforged"); r.status != 404 {
		t.Fatalf("forged link: %d", r.status)
	}
	secrets = append(secrets, "SECRETforged")
	// A link whose blob is missing on disk: nginx fails to open the file.
	dl, err := ts.Service.Download(ctx, adminOf(ts), "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(ts.DataDir, "blobs", filepath.FromSlash(dl.BlobPath))); err != nil {
		t.Fatal(err)
	}
	if r := n.get("GET", n.https+query(*broken.URL)); r.status != 404 {
		t.Fatalf("link to a missing blob: %d", r.status)
	}
	// velad down: nginx cannot reach the upstream.
	ts.HTTP.Close()
	if r := n.get("GET", n.https+"/s/01jaaaaaaaaaaaaaaaaaaaaaaa/x.pdf?exp=99999999999&sig=SECRETupstreamdown"); r.status != 502 {
		t.Fatalf("link with velad down: %d", r.status)
	}
	secrets = append(secrets, "SECRETupstreamdown")
	// A non-link request with velad down proves error logging is active.
	if r := n.get("GET", n.https+"/healthz?probe=1"); r.status != 502 {
		t.Fatalf("healthz with velad down: %d", r.status)
	}

	logs := n.logs()
	for _, s := range secrets {
		if s != "" && strings.Contains(logs, s) {
			t.Errorf("a log contains the signature %q", s)
		}
	}
	if strings.Contains(logs, "exp=") {
		t.Error("a log contains a signed-link query")
	}
	if !strings.Contains(logs, "GET /s/") || !strings.Contains(logs, "connect() failed") {
		t.Errorf("logging is not working at all, so the test proves nothing:\n%s", logs)
	}
}

// TestSingleSecurityHeaders covers R-031: every kind of response carries
// X-Content-Type-Options and Strict-Transport-Security exactly once.
func TestSingleSecurityHeaders(t *testing.T) {
	ts, n, c := setup(t)
	ctx := context.Background()
	upload(t, c, "pub.txt", "p", api.Public, "")
	upload(t, c, "priv.txt", "s", api.Private, "")
	l, err := c.CreateLink(ctx, api.CreateLinkRequest{Path: "priv.txt"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(*l.URL)
	auth := []string{"Authorization", "Bearer " + ts.AdminKey}
	check := func(what string, r response) {
		t.Helper()
		for _, h := range []string{"X-Content-Type-Options", "Strict-Transport-Security"} {
			if got := len(r.header.Values(h)); got != 1 {
				t.Errorf("%s (status %d): %s appears %d times", what, r.status, h, got)
			}
		}
	}
	check("index", n.get("GET", n.https+"/"))
	check("velad 404", n.get("GET", n.https+"/missing"))
	check("api", n.get("GET", n.https+"/api/v1/info", auth...))
	check("api error", n.get("GET", n.https+"/api/v1/info"))
	check("asset", n.get("GET", n.https+"/favicon.ico"))
	check("health", n.get("GET", n.https+"/healthz"))
	check("public blob", n.get("GET", n.https+"/pub.txt"))
	check("signed link", n.get("GET", n.https+u.RequestURI()))
	check("api download", n.get("GET", n.https+api.ContentRoute("priv.txt"), auth...))
	check("nginx 404", n.get("GET", n.https+"/_blobs/"))
	ts.HTTP.Close()
	check("nginx 502", n.get("GET", n.https+"/"))
	check("nginx 502 api", n.get("GET", n.https+"/api/v1/info", auth...))
	check("nginx 502 link", n.get("GET", n.https+u.RequestURI()))
}

// TestValidatorsAfterSameSizeOverwrite covers R-013 through nginx: same-size
// content replaced within the same second (forced by equal mtimes) must not
// revalidate as the old version, by ETag or by If-Range.
func TestValidatorsAfterSameSizeOverwrite(t *testing.T) {
	ts, n, c := setup(t)
	first := upload(t, c, "same.txt", "AAAA", api.Public, "")
	r := n.get("GET", n.https+"/same.txt")
	old := r.header.Get("ETag")
	if old != `"sha256:`+first.SHA256+`"` || len(r.header.Values("ETag")) != 1 || r.header.Get("Last-Modified") != "" {
		t.Fatalf("validators: ETag %v, Last-Modified %q", r.header.Values("ETag"), r.header.Get("Last-Modified"))
	}
	if r := n.get("GET", n.https+"/same.txt", "If-None-Match", old); r.status != 304 || r.header.Get("ETag") != old {
		t.Fatalf("unchanged content: %d %v", r.status, r.header)
	}
	upload(t, c, "same.txt", "BBBB", "", "")
	// Give every blob the same mtime, as an overwrite within one second
	// would; only content-based validators can tell them apart.
	stamp := time.Now().Truncate(time.Second)
	_ = filepath.WalkDir(filepath.Join(ts.DataDir, "blobs"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			_ = os.Chtimes(p, stamp, stamp)
		}
		return nil
	})
	r = n.get("GET", n.https+"/same.txt", "If-None-Match", old)
	if r.status != 200 || r.body != "BBBB" {
		t.Fatalf("stale ETag revalidated: %d %q", r.status, r.body)
	}
	fresh := r.header.Get("ETag")
	r = n.get("GET", n.https+"/same.txt", "Range", "bytes=0-1", "If-Range", old)
	if r.status != 200 || r.body != "BBBB" {
		t.Fatalf("stale If-Range honored: %d %q", r.status, r.body)
	}
	r = n.get("GET", n.https+"/same.txt", "Range", "bytes=0-1", "If-Range", fresh)
	if r.status != 206 || r.body != "BB" {
		t.Fatalf("current If-Range: %d %q", r.status, r.body)
	}
	r = n.get("GET", n.https+"/same.txt", "If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	if r.status != 200 {
		t.Fatalf("If-Modified-Since answered %d", r.status)
	}
}

// TestMaximalMetadataThroughNginx covers R-014: the reference nginx accepts
// every header an upload with the largest valid metadata needs.
func TestMaximalMetadataThroughNginx(t *testing.T) {
	ts, n, _ := setup(t)
	tags := make([]string, 32)
	for i := range tags {
		tags[i] = fmt.Sprintf("%02d", i) + strings.Repeat("t", 62)
	}
	path := api.EscapePath(strings.Repeat("ж", 120) + "/" + strings.Repeat("ё", 120) + "/" + strings.Repeat("я", 120) + ".txt")
	r := n.put(ts.AdminKey, path, "x",
		api.HeaderTitle, api.EncodeHeaderText(strings.Repeat("😀", api.MaxTitleRunes)),
		api.HeaderDescription, api.EncodeHeaderText(strings.Repeat("😀", api.MaxDescriptionRunes)),
		api.HeaderTags, strings.Join(tags, ","))
	if r.status != 201 {
		t.Fatalf("maximal metadata through nginx: %d %.200s", r.status, r.body)
	}
}

func TestDirectoryPoliciesThroughNginx(t *testing.T) {
	_, n, c := setup(t)
	ctx := context.Background()
	obj := upload(t, c, "hidden/file.txt", "public content", api.Public, "")
	if _, err := c.PatchDir(ctx, "hidden/", api.DirPolicyPatch{Hidden: api.Some(true), Index: api.Some(api.IndexOn)}, velaclient.DirWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		r := n.get(method, n.https+"/hidden/file.txt")
		if r.status != 200 {
			t.Fatal(r.status)
		}
		expectHeaders(t, "hidden public file", r, map[string]string{"X-Robots-Tag": "noindex, nofollow", "Cache-Control": "public, max-age=300"})
	}
	r := n.get("GET", n.https+"/hidden/file.txt", "If-None-Match", `"sha256:`+obj.SHA256+`"`)
	if r.status != 304 {
		t.Fatal(r.status)
	}
	expectHeaders(t, "hidden conditional file", r, map[string]string{"X-Robots-Tag": "noindex, nofollow", "Cache-Control": "public, max-age=300"})
	r = n.get("GET", n.https+"/hidden/")
	if r.status != 200 || !strings.Contains(r.body, `<meta name="robots" content="noindex, nofollow">`) {
		t.Fatal(r.status, r.body)
	}
	expectHeaders(t, "hidden index", r, map[string]string{"X-Robots-Tag": "noindex, nofollow", "Cache-Control": "public, max-age=60"})
	if r := n.get("GET", n.https+"/"); strings.Contains(r.body, "hidden/") {
		t.Fatal("root leaked a hidden directory")
	}
	if _, err := c.PatchDir(ctx, "", api.DirPolicyPatch{Index: api.Some(api.IndexOff)}, velaclient.DirWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if r := n.get("GET", n.https+"/"); r.status != 404 {
		t.Fatal(r.status)
	}
	if _, err := c.PatchDir(ctx, "hidden/", api.DirPolicyPatch{NoIndex: api.Some(api.IndexOff)}, velaclient.DirWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	r = n.get("GET", n.https+"/hidden/file.txt")
	expectHeaders(t, "explicit crawler opt-in", r, map[string]string{"X-Robots-Tag": "", "Cache-Control": "public, max-age=300"})
}
