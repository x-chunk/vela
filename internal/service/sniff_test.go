package service_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

// pausedSniff keeps real SQLite and filesystem persistence, pausing only the
// backend operation that would be a remote request or response body in R2.
type pausedSniff struct {
	service.Blobs
	body    bool
	once    sync.Once
	started chan time.Time
	resume  chan struct{}
}

func (b *pausedSniff) pause(ctx context.Context) error {
	var err error
	b.once.Do(func() {
		deadline, _ := ctx.Deadline()
		b.started <- deadline
		select {
		case <-b.resume:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	return err
}

func (b *pausedSniff) Open(ctx context.Context, id string, off, length int64) (io.ReadCloser, error) {
	if !b.body {
		if err := b.pause(ctx); err != nil {
			return nil, err
		}
	}
	r, err := b.Blobs.Open(ctx, id, off, length)
	if err != nil {
		return nil, err
	}
	if b.body {
		return &pausedSniffBody{ReadCloser: r, backend: b, ctx: ctx}, nil
	}
	return r, nil
}

type pausedSniffBody struct {
	io.ReadCloser
	backend *pausedSniff
	ctx     context.Context
}

func (r *pausedSniffBody) Read(p []byte) (int, error) {
	if err := r.backend.pause(r.ctx); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p)
}

func pauseSniff(t *testing.T, e *env, body bool) *pausedSniff {
	t.Helper()
	backend := &pausedSniff{Blobs: e.storage.Backend, body: body, started: make(chan time.Time, 1), resume: make(chan struct{})}
	e.storage.Backend = backend
	e.svc = e.sibling(nil)
	return backend
}

func TestContentTypeResetDoesNotHoldWriter(t *testing.T) {
	for _, body := range []bool{false, true} {
		for _, conditional := range []bool{false, true} {
			t.Run(map[bool]string{false: "open", true: "body"}[body]+map[bool]string{false: " unconditional", true: " conditional"}[conditional], func(t *testing.T) {
				e := newEnv(t)
				old := e.put("data", "old text", api.Private)
				backend := pauseSniff(t, e, body)
				var revision int64
				if conditional {
					revision = old.Revision
				}
				type result struct {
					object api.Object
					err    error
				}
				done := make(chan result, 1)
				ctx, cancel := context.WithCancel(e.ctx)
				defer cancel()
				go func() {
					o, err := e.svc.Patch(ctx, e.admin, "data", api.ObjectPatch{ContentType: api.Null[string]()}, revision)
					done <- result{o, err}
				}()
				select {
				case deadline := <-backend.started:
					if deadline.IsZero() || time.Until(deadline) > 10*time.Second {
						t.Fatal("sniff lacks bounded context")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("sniff did not start")
				}
				writeCtx, stop := context.WithTimeout(e.ctx, time.Second)
				_, err := e.svc.CreateKey(writeCtx, "unrelated", api.ScopeRO, "")
				stop()
				if err != nil {
					t.Fatalf("slow backend held SQLite writer: %v", err)
				}
				// Change the immutable blob and revision while the old read is paused.
				current := e.put("data", "%PDF-1.7\nnew contents", api.Private)
				close(backend.resume)
				select {
				case got := <-done:
					if conditional {
						wantErr(t, got.err, service.ErrPreconditionFailed)
					} else if got.err != nil || got.object.SHA256 != current.SHA256 || got.object.ContentType != "application/pdf" || got.object.Revision != current.Revision+1 {
						t.Fatalf("reset applied to stale blob: %+v, %v", got.object, got.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("reset did not finish")
				}
			})
		}
	}
}

func TestContentTypeResetReadCancellation(t *testing.T) {
	e := newEnv(t)
	old := e.put("data", "contents", api.Private)
	pauseSniff(t, e, true)
	ctx, cancel := context.WithTimeout(e.ctx, 100*time.Millisecond)
	defer cancel()
	_, err := e.svc.Patch(ctx, e.admin, "data", api.ObjectPatch{ContentType: api.Null[string]()}, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reset error=%v", err)
	}
	current, err := e.svc.Stat(e.ctx, e.admin, "data")
	if err != nil || current.Revision != old.Revision {
		t.Fatalf("failed reset changed metadata: %+v, %v", current, err)
	}
}
