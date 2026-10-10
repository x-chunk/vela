//go:build e2e

// Package e2e checks content delivery through a real nginx running the
// reference configuration from deploy/nginx (spec section 11.3): velad
// answers with X-Accel-Redirect, and the headers it decides on must reach the
// client intact. Run with "make test-e2e"; it needs Docker and uses host
// networking, so it runs on Linux.
package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/testutil"
)

const nginxImage = "nginx:1.30-alpine"

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// startNginx runs nginx with deploy/nginx/vela.conf in front of upstream and
// returns its base URL.
func startNginx(t *testing.T, upstream, blobsDir string) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	port := freePort(t)
	conf := filepath.Join(t.TempDir(), "default.conf")
	err := os.WriteFile(conf, []byte(fmt.Sprintf(`
upstream vela {
    server %s;
    keepalive 4;
}
server {
    listen 127.0.0.1:%d;
    server_name _;
    client_max_body_size 2g;
    include /etc/nginx/snippets/vela.conf;
}
`, upstream, port)), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("vela-e2e-%d", port)
	args := []string{"run", "-d", "--rm", "--name", name, "--network", "host",
		"-v", conf + ":/etc/nginx/conf.d/default.conf:ro",
		"-v", filepath.Join(repoRoot(t), "deploy", "nginx", "vela.conf") + ":/etc/nginx/snippets/vela.conf:ro",
		"-v", blobsDir + ":/srv/vela/blobs:ro",
		nginxImage}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("nginx logs:\n%s", out)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(base + "/healthz")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return base
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("nginx did not become ready")
	return ""
}

type response struct {
	status int
	header http.Header
	body   string
}

func get(t *testing.T, method, rawURL string, headers ...string) response {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return response{status: res.StatusCode, header: res.Header, body: string(b)}
}

func expectHeaders(t *testing.T, what string, h http.Header, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got := strings.Join(h.Values(k), ", "); got != v {
			t.Errorf("%s: %s = %q; want %q", what, k, got, v)
		}
	}
	// Internal headers must never leak to the client.
	for _, k := range []string{"X-Accel-Redirect", "X-Vela-Cache-Control", "X-Vela-Robots", "X-Vela-Referrer-Policy"} {
		if v := h.Get(k); v != "" {
			t.Errorf("%s: internal header %s leaked: %q", what, k, v)
		}
	}
}

func TestAccelThroughNginx(t *testing.T) {
	ts := testutil.NewServer(t, testutil.WithAccel("/_blobs/"))
	upstream := strings.TrimPrefix(ts.URL, "http://")
	base := startNginx(t, upstream, filepath.Join(ts.DataDir, "blobs"))
	ctx := context.Background()

	// Uploads go through nginx too: the API location must stream bodies.
	c, err := client.New(base, ts.AdminKey)
	if err != nil {
		t.Fatal(err)
	}
	put := func(path, content string, vis api.Visibility, d api.Disposition) *api.Object {
		o, _, err := c.Upload(ctx, path, strings.NewReader(content), client.UploadOptions{Size: int64(len(content)), Visibility: vis, Disposition: d})
		if err != nil {
			t.Fatalf("upload %s through nginx: %v", path, err)
		}
		return o
	}
	put("docs/Контрольная Q1.pdf", "%PDF-1.4 public content", api.Public, api.Attachment)
	put("docs/unlisted.txt", "unlisted content", api.Unlisted, "")
	put("docs/private.txt", "private content", api.Private, "")
	big := strings.Repeat("0123456789", 300_000)
	put("big.bin", big, api.Public, "")

	t.Run("public", func(t *testing.T) {
		r := get(t, "GET", base+"/docs/"+url.PathEscape("Контрольная Q1.pdf"))
		if r.status != 200 || r.body != "%PDF-1.4 public content" {
			t.Fatalf("status %d body %q", r.status, r.body)
		}
		expectHeaders(t, "public", r.header, map[string]string{
			"Content-Type":           "application/pdf",
			"Content-Disposition":    `attachment; filename="___________ Q1.pdf"; filename*=UTF-8''%D0%9A%D0%BE%D0%BD%D1%82%D1%80%D0%BE%D0%BB%D1%8C%D0%BD%D0%B0%D1%8F%20Q1.pdf`,
			"Cache-Control":          "public, max-age=300",
			"X-Robots-Tag":           "",
			"Referrer-Policy":        "",
			"X-Content-Type-Options": "nosniff",
		})
		if r.header.Get("ETag") == "" || r.header.Get("Last-Modified") == "" || r.header.Get("X-Request-ID") == "" {
			t.Errorf("ETag/Last-Modified/X-Request-ID missing: %v", r.header)
		}
		cond := get(t, "GET", base+"/docs/"+url.PathEscape("Контрольная Q1.pdf"), "If-None-Match", r.header.Get("ETag"))
		if cond.status != http.StatusNotModified {
			t.Errorf("conditional GET status = %d", cond.status)
		}
	})

	t.Run("range and head", func(t *testing.T) {
		r := get(t, "GET", base+"/big.bin", "Range", "bytes=10-19")
		if r.status != http.StatusPartialContent || r.body != "0123456789" || r.header.Get("Content-Range") != fmt.Sprintf("bytes 10-19/%d", len(big)) {
			t.Fatalf("range: status %d body %q headers %v", r.status, r.body, r.header)
		}
		r = get(t, "HEAD", base+"/big.bin")
		if r.status != 200 || r.body != "" || r.header.Get("Content-Length") != fmt.Sprint(len(big)) {
			t.Fatalf("head: status %d length %q", r.status, r.header.Get("Content-Length"))
		}
		r = get(t, "GET", base+"/big.bin")
		if len(r.body) != len(big) {
			t.Fatalf("full body length = %d", len(r.body))
		}
	})

	t.Run("unlisted", func(t *testing.T) {
		r := get(t, "GET", base+"/docs/unlisted.txt")
		if r.status != 200 || r.body != "unlisted content" {
			t.Fatalf("status %d body %q", r.status, r.body)
		}
		expectHeaders(t, "unlisted", r.header, map[string]string{
			"Content-Type":  "text/plain; charset=utf-8",
			"Cache-Control": "public, max-age=300",
			"X-Robots-Tag":  "noindex, nofollow",
		})
	})

	t.Run("private and internal", func(t *testing.T) {
		if r := get(t, "GET", base+"/docs/private.txt"); r.status != 404 {
			t.Fatalf("private by direct URL: %d", r.status)
		}
		for _, p := range []string{"/_blobs/", "/_blobs/aa/bb/" + strings.Repeat("0", 26)} {
			if r := get(t, "GET", base+p); r.status != 404 {
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
		r := get(t, "GET", base+u.RequestURI())
		if r.status != 200 || r.body != "private content" {
			t.Fatalf("status %d body %q", r.status, r.body)
		}
		expectHeaders(t, "signed link", r.header, map[string]string{
			"Cache-Control":   "private, no-store",
			"X-Robots-Tag":    "noindex, nofollow",
			"Referrer-Policy": "no-referrer",
			"Content-Type":    "text/plain; charset=utf-8",
		})
	})

	t.Run("api download", func(t *testing.T) {
		r := get(t, "GET", base+api.ContentRoute("docs/private.txt"), "Authorization", "Bearer "+ts.AdminKey)
		if r.status != 200 || r.body != "private content" {
			t.Fatalf("status %d body %q", r.status, r.body)
		}
		expectHeaders(t, "api download", r.header, map[string]string{
			"Cache-Control":   "private, no-store",
			"Referrer-Policy": "no-referrer",
		})
	})

	t.Run("index", func(t *testing.T) {
		r := get(t, "GET", base+"/docs/")
		if r.status != 200 || !strings.Contains(r.body, "Контрольная Q1.pdf") || strings.Contains(r.body, "unlisted.txt") {
			t.Fatalf("index: %d", r.status)
		}
		if r.header.Get("Content-Security-Policy") == "" {
			t.Error("index without CSP")
		}
	})
}
