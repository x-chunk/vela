package service

import (
	"context"
	"errors"
	"time"
)

// DefaultLockLease is how long a cross-process lock stays valid without
// renewal: the time a crashed holder can block others.
const DefaultLockLease = time.Hour

// errLockLost cancels the work of a holder that lost its lease.
var errLockLost = errors.New("lock lost to another process")

// heldLock is a cross-process lock that renews itself in the background.
type heldLock struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	stopped chan struct{}
	release func()
}

// holdLock takes the named lock for this process and keeps renewing it every
// LockLease/3, so it never expires while the holder works, however long that
// takes. If renewal fails for long enough that the lease may have expired,
// or another process turns out to hold the lock, the returned context is
// canceled with errLockLost and the work must stop. ok is false if another
// process holds the lock.
func (s *Service) holdLock(ctx context.Context, name string) (h *heldLock, ok bool, err error) {
	lease := s.opts.LockLease
	acquire := func(ctx context.Context) (bool, error) {
		now := s.clock.Now()
		var got bool
		err := s.store.Update(ctx, func(w Writer) error {
			var err error
			got, err = w.AcquireLock(ctx, name, s.owner, now, now.Add(lease))
			return err
		})
		return got, err
	}
	got, err := acquire(ctx)
	if err != nil || !got {
		return nil, false, err
	}
	lctx, cancel := context.WithCancelCause(ctx)
	h = &heldLock{ctx: lctx, cancel: cancel, stopped: make(chan struct{})}
	go func() {
		defer close(h.stopped)
		t := time.NewTicker(max(lease/3, time.Millisecond))
		defer t.Stop()
		lastRenewal := s.clock.Now()
		for {
			select {
			case <-lctx.Done():
				return
			case <-t.C:
			}
			got, err := acquire(lctx)
			switch {
			case err == nil && got:
				lastRenewal = s.clock.Now()
			case err == nil:
				s.log.Error("lock taken over by another process; stopping", "lock", name)
				cancel(errLockLost)
				return
			case lctx.Err() != nil:
				return
			default:
				s.log.Warn("lock renewal failed", "lock", name, "error", err)
				// Stop before the lease can expire unnoticed.
				if s.clock.Now().Sub(lastRenewal) > lease*2/3 {
					cancel(errLockLost)
					return
				}
			}
		}
	}()
	h.release = func() {
		cancel(context.Canceled)
		<-h.stopped
		// A fresh context: the work may have ended because ctx was canceled,
		// and the lock should still be dropped.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer rcancel()
		if err := s.store.Update(rctx, func(w Writer) error { return w.ReleaseLock(rctx, name, s.owner) }); err != nil {
			s.log.Warn("could not release lock", "lock", name, "error", err)
		}
	}
	return h, true, nil
}

// lost reports whether the lock was lost while held.
func (h *heldLock) lost() bool { return errors.Is(context.Cause(h.ctx), errLockLost) }
