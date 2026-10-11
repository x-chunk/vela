//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	velaclient "go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/testutil"
)

// startCachingProxy models a CDN in front of direct delivery. Its URI-only
// cache key intentionally lets a Range response affect a later ordinary GET;
// Cache-Control must prevent an error from being stored under that key.
func startCachingProxy(t *testing.T, upstream string) *nginx {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	port := freePort(t)
	conf := fmt.Sprintf(`proxy_cache_path /tmp/vela-cache keys_zone=vela:1m;
server {
 listen 127.0.0.1:%d;
 location / {
  proxy_pass %s;
  proxy_cache vela;
  proxy_cache_key $uri;
  proxy_cache_valid 200 416 5m;
  proxy_set_header Range $http_range;
  proxy_ignore_headers X-Accel-Buffering;
  add_header X-Cache $upstream_cache_status always;
 }
}
`, port, upstream)
	confPath := filepath.Join(t.TempDir(), "default.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("vela-cache-e2e-%d", port)
	args := []string{"run", "-d", "--rm", "--name", name, "--network", "host", "-v", confPath + ":/etc/nginx/conf.d/default.conf:ro", nginxImage}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	n := &nginx{t: t, name: name, http: fmt.Sprintf("http://127.0.0.1:%d", port)}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("cache proxy logs:\n%s", out)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := n.try("GET", n.http+"/healthz"); err == nil && r.status == 200 {
			return n
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("cache proxy did not become ready")
	return nil
}

func TestRangeErrorDoesNotPoisonProxyCache(t *testing.T) {
	ts := testutil.NewServer(t)
	c, err := velaclient.New(ts.URL, ts.AdminKey)
	if err != nil {
		t.Fatal(err)
	}
	n := startCachingProxy(t, ts.URL)
	for _, visibility := range []api.Visibility{api.Public, api.Unlisted} {
		for _, value := range []string{"bytes=100-", "bytes=bad"} {
			t.Run(string(visibility)+"/"+value, func(t *testing.T) {
				path := string(visibility) + "-" + strings.TrimPrefix(value, "bytes=") + ".txt"
				upload(t, c, path, "0123456789", visibility, api.Inline)
				url := n.http + "/" + path
				bad := n.get("GET", url, "Range", value)
				if bad.status != 416 || bad.header.Get("X-Cache") != "MISS" {
					t.Fatalf("invalid range: %d %v", bad.status, bad.header)
				}
				full := n.get("GET", url)
				if full.status != 200 || full.body != "0123456789" || full.header.Get("X-Cache") != "MISS" {
					t.Fatalf("GET after invalid Range: %d cache=%s body=%q", full.status, full.header.Get("X-Cache"), full.body)
				}
				if bad.header.Get("Cache-Control") != "no-store" || bad.header.Get("ETag") != "" || bad.header.Get("Content-Range") != "bytes */10" {
					t.Fatalf("error policy: %v", bad.header)
				}
				cached := n.get("GET", url)
				if cached.status != 200 || cached.body != full.body || cached.header.Get("X-Cache") != "HIT" {
					t.Fatalf("successful content was not cached: %d %v", cached.status, cached.header)
				}
			})
		}
	}
}
