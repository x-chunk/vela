package service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DefaultBlobGrace is how long replaced, purged or orphaned content stays on
// disk before it is deleted.
const DefaultBlobGrace = 15 * time.Minute

// Blob deletion is deferred, for two reasons.
//
// Durability: the database runs with synchronous=NORMAL, so a committed
// transaction can still be rolled back by a power loss until the WAL is
// synced. If the blob an overwrite or purge replaced were unlinked right
// after the commit, a crash could resurrect a row that points at a deleted
// file. Instead the blob is queued in blob_gc by the same transaction, and
// the collector unlinks it only after a FULL checkpoint, which makes every
// earlier commit durable. If the transaction is lost, so is its queue entry,
// and the still referenced blob is never deleted.
//
// Readers: resolving an object and opening its blob are separate steps (and
// in accel mode nginx opens the file after velad answered). The grace period
// keeps the old content readable for requests that resolved it just before
// an overwrite.
//
// A blob, once unreferenced, never becomes referenced again: new content
// always gets a new ID. The collector still re-checks references and skips
// any blob that is referenced, so a stray queue entry cannot destroy data.

// queueBlob schedules a blob that the current transaction stops referencing.
func (s *Service) queueBlob(ctx context.Context, w Writer, blobID string, now time.Time) error {
	_, err := w.EnqueueBlobDeletion(ctx, blobID, now.Add(s.opts.BlobGrace))
	return err
}

// CollectBlobs deletes queued blobs whose grace period has passed and
// returns how many files it removed.
func (s *Service) CollectBlobs(ctx context.Context) (int, error) {
	// Runs are serialized within the process; across processes the
	// collector's transaction is the serialization point.
	s.gcMu.Lock()
	defer s.gcMu.Unlock()
	const batch = 500
	now := s.now()
	due, err := s.store.DueBlobDeletions(ctx, now, 1)
	if err != nil || len(due) == 0 {
		return 0, err
	}
	// Every due entry was queued at least BlobGrace ago, so the commit that
	// queued it precedes this checkpoint, which makes it durable.
	if err := s.store.Checkpoint(ctx); err != nil {
		return 0, fmt.Errorf("durability barrier not reached, deletion postponed: %w", err)
	}
	removed := 0
	for {
		var doomed []string
		err := s.store.Update(ctx, func(w Writer) error {
			// A backup copies blobs after its snapshot; deleting now could
			// remove a blob the snapshot references. The check is inside
			// the write transaction, which serializes it with the backup
			// taking its lock.
			if held, err := w.LockHeld(ctx, backupLock, s.clock.Now()); err != nil || held {
				if held {
					err = ErrBackupRunning
				}
				return err
			}
			ids, err := w.DueBlobDeletions(ctx, now, batch)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := w.DeleteBlobDeletion(ctx, id); err != nil {
					return err
				}
				used, err := w.BlobReferenced(ctx, id)
				if err != nil {
					return err
				}
				if !used {
					doomed = append(doomed, id)
				}
			}
			if len(ids) < batch {
				ids = nil
			}
			due = ids
			return nil
		})
		if errors.Is(err, ErrBackupRunning) {
			s.log.Info("blob deletion postponed while a backup runs")
			return removed, nil
		}
		if err != nil {
			return removed, err
		}
		for _, id := range doomed {
			if err := s.blobs.Delete(ctx, id); err != nil {
				s.log.Warn("delete blob failed; the janitor will retry", "blob_id", id, "error", err)
				continue
			}
			removed++
		}
		if len(due) == 0 || ctx.Err() != nil {
			return removed, ctx.Err()
		}
	}
}

// RunBlobCollectorLoop runs CollectBlobs every interval until ctx ends, so
// that deleted content does not wait for the daily janitor.
func (s *Service) RunBlobCollectorLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n, err := s.CollectBlobs(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("blob collection failed", "error", err)
		} else if n > 0 {
			s.log.Info("blobs collected", "count", n)
		}
	}
}
