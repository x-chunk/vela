package r2

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.xchunk.org/vela/internal/content"
	"go.xchunk.org/vela/internal/ids"
)

// countedSource generates contents without an input-sized allocation, so only
// SDK buffering determines how much can be consumed before parts complete.
type countedSource struct {
	remaining int64
	read      *atomic.Int64
	progress  chan struct{}
}

func (r *countedSource) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), r.remaining))
	clear(p[:n])
	r.remaining -= int64(n)
	r.read.Add(int64(n))
	select {
	case r.progress <- struct{}{}:
	default:
	}
	return n, nil
}

func TestMultipartPayloadBufferingAcrossUploads(t *testing.T) {
	for _, uploads := range []int{1, 4} {
		t.Run(fmt.Sprint(uploads), func(t *testing.T) {
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST" && r.URL.Query().Has("uploads"):
					w.Header().Set("Content-Type", "application/xml")
					_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>test-upload</UploadId></InitiateMultipartUploadResult>`)
				case r.Method == "PUT" && r.URL.Query().Has("partNumber"):
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						return
					}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("ETag", `"part"`)
					w.WriteHeader(200)
				case r.Method == "POST" && r.URL.Query().Has("uploadId"):
					w.Header().Set("Content-Type", "application/xml")
					_, _ = io.WriteString(w, `<CompleteMultipartUploadResult><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
				default:
					w.WriteHeader(404)
				}
			}))
			defer ts.Close()
			// Unblock before server cleanup even if an assertion terminates the test.
			defer unblock()
			cfg := testConfig(ts.URL, "test")
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var consumed atomic.Int64
			progress := make(chan struct{}, 1)
			done := make(chan error, uploads)
			for range uploads {
				go func() {
					size := 7*cfg.PartSize + 1
					source := &countedSource{remaining: size, read: &consumed, progress: progress}
					_, err := s.Put(ctx, ids.NewULID(nil).New(), source, content.PutOptions{MaxSize: size, SizeHint: -1})
					done <- err
				}()
			}
			want := cfg.PartSize * int64(cfg.UploadConcurrency+2) * int64(uploads)
			for consumed.Load() < want {
				select {
				case <-progress:
				case <-ctx.Done():
					t.Fatalf("buffering stalled at %d, want %d", consumed.Load(), want)
				}
			}
			// With every part response held, the bounded pool must stop read-ahead.
			stable := time.NewTimer(100 * time.Millisecond)
			defer stable.Stop()
			for {
				if got := consumed.Load(); got != want {
					t.Fatalf("payload buffered=%d, want=%d", got, want)
				}
				select {
				case <-progress:
					continue
				case <-stable.C:
					goto buffered
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		buffered:
			unblock()
			for range uploads {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
