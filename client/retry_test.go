package client_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/testutil"
)

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

