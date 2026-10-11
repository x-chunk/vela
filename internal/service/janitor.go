package service

import (
	"context"
	"errors"
	"time"

	"go.xchunk.org/vela/internal/content"
)

const (
	// janitorLock names the cross-process advisory lock.
	janitorLock = "janitor"
	// staleAge is how old tmp files and orphan blobs must be before the
	// janitor removes them. It must exceed any plausible gap between writing
	// a blob and committing the row that references it.
	staleAge = time.Hour
	// janitorBy is recorded as deleted_by_key for objects the janitor trashes.
	janitorBy = "janitor"
)

// TaskResult is the outcome of one janitor task.
type TaskResult struct {
	Name     string
	Count    int
	Duration time.Duration
	Err      error
}

// JanitorReport is the outcome of a janitor pass.
type JanitorReport struct {
	// Skipped is true when another pass, in this or another process, was
	// already running.
	Skipped bool
	// Interrupted is true when the pass stopped early because its lock was
	// lost or its context ended.
	Interrupted bool
	Tasks       []TaskResult
}

// Err joins the task errors.
func (r JanitorReport) Err() error {
	var errs []error
	for _, t := range r.Tasks {
		if t.Err != nil {
			errs = append(errs, t.Err)
		}
	}
	return errors.Join(errs...)
}

// RunJanitor performs one cleanup pass (spec section 9). Tasks run in order
// and independently: a failing task is logged and reported, and the others
// still run. Concurrent passes are prevented by a mutex within the process
// and, across processes, by a lock row whose lease is renewed for as long as
// the pass runs; if the lease is lost anyway, the pass stops.
func (s *Service) RunJanitor(ctx context.Context) (JanitorReport, error) {
	if !s.janitorMu.TryLock() {
		return JanitorReport{Skipped: true}, nil
	}
	defer s.janitorMu.Unlock()

	lock, acquired, err := s.holdLock(ctx, janitorLock)
	if err != nil {
		return JanitorReport{}, err
	}
	if !acquired {
		s.log.Info("janitor pass skipped: another process holds the lock")
		return JanitorReport{Skipped: true}, nil
	}
	defer lock.release()
	ctx = lock.ctx
	now := s.now()

	tasks := []struct {
		name string
		run  func(context.Context, time.Time) (int, error)
	}{
		{"expired_links", s.janitorLinks},
		{"expired_objects", s.janitorExpiredObjects},
		{"trash", s.janitorTrash},
		{"signing_keys", s.janitorSigningKeys},
		{"tmp_files", s.janitorTmp},
		{"orphan_blobs", s.janitorOrphans},
		{"blob_gc", func(ctx context.Context, _ time.Time) (int, error) { return s.CollectBlobs(ctx) }},
	}
	var report JanitorReport
	for _, t := range tasks {
		if ctx.Err() != nil {
			report.Interrupted = true
			if lock.lost() {
				s.log.Error("janitor pass interrupted: lock lost")
			}
			break
		}
		if s.beforeJanitorTask != nil {
			s.beforeJanitorTask(t.name)
		}
		start := time.Now()
		n, err := t.run(ctx, now)
		res := TaskResult{Name: t.name, Count: n, Duration: time.Since(start), Err: err}
		report.Tasks = append(report.Tasks, res)
		if err != nil {
			s.log.Error("janitor task failed", "task", t.name, "count", n, "duration", res.Duration, "error", err)
		} else {
			s.log.Info("janitor task done", "task", t.name, "count", n, "duration", res.Duration)
		}
	}
	return report, nil
}

// janitorLinks drops link records that expired (or were revoked) longer ago
// than the retention period; recent ones stay visible for debugging.
func (s *Service) janitorLinks(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.store.Update(ctx, func(w Writer) error {
		var err error
		n, err = w.DeleteLinksBefore(ctx, now.Add(-s.opts.LinkRetention))
		return err
	})
	return n, err
}

// janitorExpiredObjects moves objects past their expires_at to the trash.
func (s *Service) janitorExpiredObjects(ctx context.Context, now time.Time) (int, error) {
	const batch = 500
	total := 0
	for {
		objs, err := s.store.ExpiredObjects(ctx, now, batch)
		if err != nil || len(objs) == 0 {
			return total, err
		}
		moved := 0
		err = s.store.Update(ctx, func(w Writer) error {
			for _, candidate := range objs {
				// Re-read inside the transaction: the expiry may have been
				// lifted since the candidate list was taken.
				o, err := w.ObjectByID(ctx, candidate.ID)
				if isNotFound(err) {
					continue
				}
				if err != nil {
					return err
				}
				if !o.ExpiredAt(now) {
					continue
				}
				if _, err := s.trashObject(ctx, w, o, janitorBy, now); err != nil {
					return err
				}
				moved++
			}
			return nil
		})
		total += moved
		if err != nil || len(objs) < batch || moved == 0 {
			return total, err
		}
	}
}

// janitorTrash purges trash entries older than the retention period.
func (s *Service) janitorTrash(ctx context.Context, now time.Time) (int, error) {
	return s.purgeTrashBefore(ctx, now.Add(-s.opts.TrashRetention), "")
}

// janitorSigningKeys deletes retired signing keys no live link depends on.
func (s *Service) janitorSigningKeys(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.store.Update(ctx, func(w Writer) error {
		var err error
		n, err = w.DeleteUnusedRetiredSigningKeys(ctx, now)
		return err
	})
	return n, err
}

// janitorTmp removes temporary upload files abandoned by crashes or dropped
// connections.
func (s *Service) janitorTmp(_ context.Context, now time.Time) (int, error) {
	return s.blobs.CleanTmp(now.Add(-staleAge))
}

// janitorOrphans finds blobs that no object or trash entry references: the
// leftovers of a crash between writing a blob and committing its row, or of
// a failed delete. Young blobs are skipped because they may belong to an
// upload whose transaction has not committed yet. Orphans are not deleted
// here but queued for the collector, so they get the same durability barrier
// and grace period as any other deletion (see gc.go): "unreferenced now"
// might rest on a commit that a power loss could still undo.
func (s *Service) janitorOrphans(ctx context.Context, now time.Time) (int, error) {
	cutoff := now.Add(-staleAge)
	queued := 0
	err := s.blobs.Walk(ctx, func(info content.BlobInfo) error {
		id, modTime := info.ID, info.ModTime
		if !modTime.Before(cutoff) {
			return nil
		}
		used, err := s.store.BlobReferenced(ctx, id)
		if err != nil || used {
			return err
		}
		var added bool
		err = s.store.Update(ctx, func(w Writer) error {
			var err error
			added, err = w.EnqueueBlobDeletion(ctx, id, now.Add(s.opts.BlobGrace))
			return err
		})
		if added {
			queued++ // already queued blobs (purged, replaced) are not orphans
		}
		return err
	})
	return queued, err
}

// RunJanitorLoop runs a pass after initialDelay and then every interval until
// ctx is canceled.
func (s *Service) RunJanitorLoop(ctx context.Context, initialDelay, interval time.Duration) {
	timer := time.NewTimer(initialDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, err := s.RunJanitor(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("janitor pass failed", "error", err)
		}
		timer.Reset(interval)
	}
}
