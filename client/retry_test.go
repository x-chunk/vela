package client_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/testutil"
)

// truncating proxies to target and, for the first n requests matching
// match, sends the full headers but only half of the response body, as a
// connection lost mid-response would.
func truncating(t *testing.T, target string, n int32, match func(*http.Request) bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	u, _ := url.Parse(target)
	proxy := httputil.NewSingleHostReverseProxy(u)
	var cut, seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !match(r) {
			proxy.ServeHTTP(w, r)
			return
		}
		seen.Add(1)
		if cut.Add(1) > n {
			proxy.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, r)
		body := rec.Body.Bytes()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(rec.Code)
		_, _ = w.Write(body[:len(body)/2])
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func fastClient(t *testing.T, base, key string) *client.Client {
	t.Helper()
	c, err := client.New(base, key, client.WithBackoff(func(int) time.Duration { return time.Millisecond }))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestRetryUploadFromFile covers R-019: a conditional upload from an
// *os.File is retried with the whole content, and the caller's file stays
// open.
func TestRetryUploadFromFile(t *testing.T) {
	t.Parallel()
	ts := testutil.NewServer(t)
	srv, count := flaky(t, ts.URL, 1, http.StatusServiceUnavailable)
	c := fastClient(t, srv.URL, ts.AdminKey)
	path := filepath.Join(t.TempDir(), "f.txt")
	content := strings.Repeat("file content ", 1000)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	o, created, err := c.Upload(context.Background(), "f.txt", f, client.UploadOptions{Size: int64(len(content)), CreateOnly: true, SHA256: sum(content)})
	if err != nil || !created || o.Size != int64(len(content)) || count.Load() != 2 {
		t.Fatalf("upload = %+v, %v, %v after %d requests", o, created, err, count.Load())
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("the caller's file was closed: %v", err)
	}
}

// TestRetryKeepsStartOffset covers R-020: a retry resends the body from
// where the caller positioned it, not from offset 0.
func TestRetryKeepsStartOffset(t *testing.T) {
	t.Parallel()
	ts := testutil.NewServer(t)
	srv, _ := flaky(t, ts.URL, 1, http.StatusServiceUnavailable)
	c := fastClient(t, srv.URL, ts.AdminKey)
	r := bytes.NewReader([]byte("prefixDATA"))
	if _, err := r.Seek(6, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Upload(context.Background(), "d.txt", r, client.UploadOptions{Size: -1, CreateOnly: true}); err != nil {
		t.Fatal(err)
	}
	rc, _, err := c.Download(context.Background(), "d.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	if b, _ := io.ReadAll(rc); string(b) != "DATA" {
		t.Fatalf("stored %q", b)
	}
}

// TestRetryLostResponseBody covers R-021: a response cut after its headers
// is retried for idempotent requests, including conditional uploads that
// the server already applied, and not for POST.
func TestRetryLostResponseBody(t *testing.T) {
	t.Parallel()
	ts := testutil.NewServer(t)
	ctx := context.Background()

	srv, seen := truncating(t, ts.URL, 1, func(r *http.Request) bool { return r.URL.Path == api.RouteInfo })
	if _, err := fastClient(t, srv.URL, ts.AdminKey).Info(ctx); err != nil || seen.Load() != 2 {
		t.Fatalf("GET with a cut response: %v after %d requests", err, seen.Load())
	}

	isPut := func(r *http.Request) bool { return r.Method == http.MethodPut }
	srv, seen = truncating(t, ts.URL, 1, isPut)
	c := fastClient(t, srv.URL, ts.AdminKey)
	o, created, err := c.Upload(ctx, "p.txt", strings.NewReader("one"), client.UploadOptions{Size: 3, CreateOnly: true, SHA256: sum("one")})
	if err != nil || !created || o.Revision != 1 || seen.Load() != 2 {
		t.Fatalf("create-only upload with a cut response = %+v, %v, %v after %d", o, created, err, seen.Load())
	}

	srv, seen = truncating(t, ts.URL, 1, isPut)
	c = fastClient(t, srv.URL, ts.AdminKey)
	o, created, err = c.Upload(ctx, "p.txt", strings.NewReader("two"), client.UploadOptions{Size: 3, IfRevision: 1, SHA256: sum("two")})
	if err != nil || created || o.Revision != 2 || seen.Load() != 2 {
		t.Fatalf("If-Match upload with a cut response = %+v, %v, %v after %d", o, created, err, seen.Load())
	}

	srv, seen = truncating(t, ts.URL, 1, func(r *http.Request) bool { return r.Method == http.MethodPost })
	_, err = fastClient(t, srv.URL, ts.AdminKey).Create(ctx, api.CreateObjectRequest{Path: "c.txt", Content: "x"})
	var ne *client.NetworkError
	if !errorsAs(err, &ne) || seen.Load() != 1 {
		t.Fatalf("POST with a cut response: %v after %d requests (must not be retried)", err, seen.Load())
	}
}
