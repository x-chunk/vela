package client_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/testutil"
)

func newClient(t *testing.T, ts *testutil.Server, opts ...client.Option) *client.Client {
	t.Helper()
	opts = append([]client.Option{client.WithBackoff(func(int) time.Duration { return time.Millisecond })}, opts...)
	c, err := client.New(ts.URL, ts.AdminKey, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestEndToEnd(t *testing.T) {
	t.Parallel()
	ts := testutil.NewServer(t)
	c := newClient(t, ts)
	ctx := context.Background()

	info, err := c.CheckServer(ctx)
	if err != nil || info.Key.Scope != api.ScopeAdmin {
		t.Fatalf("CheckServer = %+v, %v", info, err)
	}

	title := "Отчёт 50%"
	exp := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	o, created, err := c.Upload(ctx, "/docs/report q1.txt", strings.NewReader("hello"), client.UploadOptions{
		Size: 5, Visibility: api.Public, Title: &title, Tags: []string{"a", "b"},
		Disposition: api.Attachment, ExpiresAt: &exp, SHA256: sum("hello"), CreateOnly: true,
	})
	if err != nil || !created || o.Title != title || !o.ExpiresAt.Equal(exp) || o.Path != "docs/report q1.txt" {
		t.Fatalf("Upload = %+v, %v, %v", o, created, err)
	}
	_, _, err = c.Upload(ctx, "docs/report q1.txt", strings.NewReader("x"), client.UploadOptions{Size: 1, CreateOnly: true})
	if !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("second CreateOnly upload = %v", err)
	}
	var ae *api.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 409 || ae.RequestID == "" {
		t.Fatalf("api error = %+v", ae)
	}

	o, created, err = c.Upload(ctx, "docs/report q1.txt", strings.NewReader("hello again"), client.UploadOptions{Size: -1, IfRevision: 1, ClearExpiry: true})
	if err != nil || created || o.Revision != 2 || o.ExpiresAt != nil {
		t.Fatalf("overwrite = %+v, %v, %v", o, created, err)
	}

	rc, dl, err := c.Download(ctx, "docs/report q1.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(b) != "hello again" || dl.SHA256 != sum("hello again") || dl.Size != 11 {
		t.Fatalf("Download = %q %+v", b, dl)
	}

	created2, err := c.Create(ctx, api.CreateObjectRequest{Path: "notes/a.md", Content: "# a"})
	if err != nil || created2.ContentType != "text/markdown; charset=utf-8" {
		t.Fatalf("Create = %+v, %v", created2, err)
	}

	l, err := c.List(ctx, client.ListOptions{})
	if err != nil || strings.Join(l.Dirs, ",") != "docs/,notes/" {
		t.Fatalf("List = %+v, %v", l, err)
	}
	all, err := c.ListAll(ctx, client.ListOptions{Recursive: true, Limit: 1})
	if err != nil || len(all.Objects) != 2 {
		t.Fatalf("ListAll = %+v, %v", all, err)
	}

	st, err := c.Stat(ctx, "docs/report q1.txt")
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Patch(ctx, "docs/report q1.txt", api.ObjectPatch{Visibility: api.Some(api.Private), Title: api.Null[string]()}, st.Revision)
	if err != nil || p.Visibility != api.Private || p.Title != "" || p.URL != nil {
		t.Fatalf("Patch = %+v, %v", p, err)
	}
	if _, err := c.Patch(ctx, "docs/report q1.txt", api.ObjectPatch{Title: api.Some("x")}, st.Revision); !api.IsCode(err, api.CodePreconditionFailed) {
		t.Fatalf("stale Patch = %v", err)
	}

	mv, err := c.Move(ctx, api.MoveRequest{From: "docs/", To: "archive/", Recursive: true})
	if err != nil || mv.Moved != 1 {
		t.Fatalf("Move = %+v, %v", mv, err)
	}
	del, err := c.Delete(ctx, "archive/", client.DeleteOptions{Recursive: true})
	if err != nil || del.Deleted != 1 || len(del.TrashIDs) != 1 {
		t.Fatalf("Delete = %+v, %v", del, err)
	}
	if _, err := c.Stat(ctx, "missing"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("Stat(missing) = %v", err)
	}
}

// flaky fronts a test server and fails the first n requests with status.
func flaky(t *testing.T, target string, n int32, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	u, _ := url.Parse(target)
	proxy := httputil.NewSingleHostReverseProxy(u)
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) <= n {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Retry-After", "0")
			http.Error(w, "flaky", status)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

func TestRetries(t *testing.T) {
	t.Parallel()
	ts := testutil.NewServer(t)
	ctx := context.Background()

	srv, count := flaky(t, ts.URL, 2, http.StatusServiceUnavailable)
	c, _ := client.New(srv.URL, ts.AdminKey, client.WithBackoff(func(int) time.Duration { return time.Millisecond }))
	if _, err := c.Info(ctx); err != nil || count.Load() != 3 {
		t.Fatalf("GET after 2 failures: %v, %d attempts", err, count.Load())
	}

	// Non-idempotent requests are not retried.
	srv, count = flaky(t, ts.URL, 1, http.StatusBadGateway)
	c, _ = client.New(srv.URL, ts.AdminKey, client.WithBackoff(func(int) time.Duration { return time.Millisecond }))
	_, err := c.Create(ctx, api.CreateObjectRequest{Path: "x", Content: "x"})
	if !api.IsCode(err, api.CodeUnavailable) || count.Load() != 1 {
		t.Fatalf("POST retried: %v, %d attempts", err, count.Load())
	}

	// A conditional upload from a seekable body is retried.
	srv, count = flaky(t, ts.URL, 1, http.StatusInternalServerError)
	c, _ = client.New(srv.URL, ts.AdminKey, client.WithBackoff(func(int) time.Duration { return time.Millisecond }))
	o, created, err := c.Upload(ctx, "seek.txt", bytes.NewReader([]byte("data")), client.UploadOptions{Size: 4, CreateOnly: true})
	if err != nil || !created || o.Size != 4 || count.Load() != 2 {
		t.Fatalf("retried upload = %+v, %v, %d attempts", o, err, count.Load())
	}

	// A plain overwrite is not retried.
	srv, count = flaky(t, ts.URL, 1, http.StatusInternalServerError)
	c, _ = client.New(srv.URL, ts.AdminKey, client.WithBackoff(func(int) time.Duration { return time.Millisecond }))
	if _, _, err := c.Upload(ctx, "seek.txt", bytes.NewReader([]byte("new")), client.UploadOptions{Size: 3}); err == nil || count.Load() != 1 {
		t.Fatalf("unconditional upload retried: %v, %d", err, count.Load())
	}

	// Errors without an API body still get a code from the status.
	srv, _ = flaky(t, ts.URL, 100, http.StatusRequestEntityTooLarge)
	c, _ = client.New(srv.URL, ts.AdminKey, client.WithRetries(0))
	if _, err := c.Info(ctx); !api.IsCode(err, api.CodePayloadTooLarge) {
		t.Fatalf("plain 413 = %v", err)
	}
}

// lostResponse lets the first PUT reach the server but drops its response,
// as a broken connection would.
func TestUploadRetryAfterLostResponse(t *testing.T) {
	t.Parallel()
	ts := testutil.NewServer(t)
	u, _ := url.Parse(ts.URL)
	proxy := httputil.NewSingleHostReverseProxy(u)
	var puts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && puts.Add(1) == 1 {
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, r)
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL, ts.AdminKey, client.WithBackoff(func(int) time.Duration { return time.Millisecond }))
	o, created, err := c.Upload(context.Background(), "once.txt", strings.NewReader("payload"), client.UploadOptions{
		Size: 7, CreateOnly: true, SHA256: sum("payload"),
	})
	if err != nil || !created || o.SHA256 != sum("payload") || puts.Load() != 2 {
		t.Fatalf("upload after lost response = %+v, %v, %v, %d PUTs", o, created, err, puts.Load())
	}
}

func TestTraceAndUserAgent(t *testing.T) {
	t.Parallel()
	ts := testutil.NewServer(t)
	var trace bytes.Buffer
	var ua atomic.Value
	u, _ := url.Parse(ts.URL)
	proxy := httputil.NewSingleHostReverseProxy(u)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua.Store(r.Header.Get("User-Agent"))
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL, ts.AdminKey, client.WithTrace(&trace), client.WithUserAgent("vela-cli/test (linux/amd64)"))
	if _, err := c.Info(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ua.Load() != "vela-cli/test (linux/amd64)" {
		t.Fatalf("User-Agent = %v", ua.Load())
	}
	out := trace.String()
	if !strings.Contains(out, "-> GET "+srv.URL+"/api/v1/info") || !strings.Contains(out, "<- 200 OK") {
		t.Fatalf("trace = %q", out)
	}
	if strings.Contains(out, ts.AdminKey[16:]) {
		t.Fatal("trace leaked the API key")
	}
}

func TestNetworkErrorsAndValidation(t *testing.T) {
	t.Parallel()
	if _, err := client.New("store.xchunk.org", "k"); err == nil {
		t.Fatal("URL without scheme accepted")
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	c, _ := client.New(addr, "k", client.WithRetries(1), client.WithBackoff(func(int) time.Duration { return time.Millisecond }))
	_, err := c.Info(context.Background())
	var ne *client.NetworkError
	if !errors.As(err, &ne) {
		t.Fatalf("closed server error = %T %v", err, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Info(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v", err)
	}
}
