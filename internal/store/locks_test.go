package store

import (
	"context"
	"testing"
	"time"
)

func TestLocks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	acquire := func(owner string, now time.Time) bool {
		var ok bool
		err := s.Update(ctx, func(tx *Tx) error {
			var err error
			ok, err = tx.AcquireLock(ctx, "janitor", owner, now, now.Add(time.Hour))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !acquire("a", t0) {
		t.Fatal("first acquire failed")
	}
	if acquire("b", t0.Add(time.Minute)) {
		t.Fatal("second owner acquired a held lock")
	}
	if !acquire("a", t0.Add(time.Minute)) {
		t.Fatal("owner could not extend its lock")
	}
	if !acquire("b", t0.Add(2*time.Hour)) {
		t.Fatal("expired lock not taken over")
	}
	release := func(owner string) {
		if err := s.Update(ctx, func(tx *Tx) error { return tx.ReleaseLock(ctx, "janitor", owner) }); err != nil {
			t.Fatal(err)
		}
	}
	release("a") // not the holder: no effect
	if acquire("c", t0.Add(2*time.Hour)) {
		t.Fatal("release by non-holder dropped the lock")
	}
	release("b")
	if !acquire("c", t0.Add(2*time.Hour)) {
		t.Fatal("lock not free after release")
	}
}
