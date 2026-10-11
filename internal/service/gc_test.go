package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/store"
)

// TestCollectorKeepsReferencedBlobs covers the R-002 safety net: a queue
// entry for a blob that is referenced (as after a crash rolled back the
// transaction that dropped the reference but kept the entry) must not
// destroy the content.
func TestCollectorKeepsReferencedBlobs(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("keep.txt", "precious", api.Public)
	st, _ := e.svc.Stat(e.ctx, e.admin, "keep.txt")
	c, _ := e.svc.Download(e.ctx, e.admin, "keep.txt")
	err := e.storage.DB.Update(e.ctx, func(tx *store.Tx) error {
		_, err := tx.EnqueueBlobDeletion(e.ctx, c.Object.BlobID, e.clock.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.collectAfterGrace(); n != 0 {
		t.Fatalf("collector deleted %d referenced blob(s)", n)
	}
	if got := e.read(c); got != "precious" || st.SHA256 == "" {
		t.Fatalf("content after collection = %q", got)
	}
	// The stray entry is gone, so it cannot fire later either.
	due, _ := e.storage.DB.DueBlobDeletions(e.ctx, e.clock.Now().Add(time.Hour), 10)
	if len(due) != 0 {
		t.Fatalf("queue not drained: %v", due)
	}
}

func TestCollectorWaitsForGrace(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("x", "1", api.Public)
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "x", Purge: true}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(service.DefaultBlobGrace - time.Second)
	if n, err := e.svc.CollectBlobs(e.ctx); err != nil || n != 0 {
		t.Fatalf("collected before grace: %d, %v", n, err)
	}
	if n := e.collectAfterGrace(); n != 1 {
		t.Fatalf("collected after grace = %d", n)
	}
}

type flakyDeletion struct {
	service.Blobs
	fail bool
}

func (b *flakyDeletion) Delete(ctx context.Context, id string) error {
	if b.fail {
		return errors.New("temporary backend outage")
	}
	return b.Blobs.Delete(ctx, id)
}

func TestCollectorKeepsQueueAfterBackendFailure(t *testing.T) {
	e := newEnv(t)
	e.put("queued.txt", "contents", api.Public)
	c, err := e.svc.Download(e.ctx, e.admin, "queued.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "queued.txt", Purge: true}); err != nil {
		t.Fatal(err)
	}
	flaky := &flakyDeletion{Blobs: e.storage.Backend, fail: true}
	e.storage.Backend = flaky
	svc := e.sibling(nil)
	e.clock.Advance(service.DefaultBlobGrace + time.Second)
	if n, err := svc.CollectBlobs(e.ctx); err == nil || n != 0 {
		t.Fatalf("outage: %d %v", n, err)
	}
	due, err := e.storage.DB.DueBlobDeletions(e.ctx, e.clock.Now(), 10)
	if err != nil || len(due) != 1 || due[0] != c.Object.BlobID {
		t.Fatalf("retry queue: %v %v", due, err)
	}
	flaky.fail = false
	if n, err := svc.CollectBlobs(e.ctx); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	due, err = e.storage.DB.DueBlobDeletions(e.ctx, e.clock.Now(), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("queue after successful retry: %v %v", due, err)
	}
}
