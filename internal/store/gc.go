package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrCheckpointBusy means a checkpoint could not copy the whole WAL into the
// database file, so the durability of recent commits is not established.
var ErrCheckpointBusy = errors.New("wal checkpoint incomplete")

// EnqueueBlobDeletion schedules the physical deletion of a blob no earlier
// than notBefore and reports whether it was newly queued; queuing an already
// queued blob keeps the existing entry.
func (t *Tx) EnqueueBlobDeletion(ctx context.Context, blobID string, notBefore time.Time) (bool, error) {
	res, err := t.tx.ExecContext(ctx, `INSERT INTO blob_gc (blob_id, not_before) VALUES (?, ?)
		ON CONFLICT (blob_id) DO NOTHING`, blobID, ms(notBefore))
	if err != nil {
		return false, fmt.Errorf("queue blob deletion: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteBlobDeletion removes a queue entry.
func (t *Tx) DeleteBlobDeletion(ctx context.Context, blobID string) error {
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM blob_gc WHERE blob_id = ?`, blobID); err != nil {
		return fmt.Errorf("dequeue blob deletion: %w", err)
	}
	return nil
}

// DueBlobDeletions returns up to limit queued blobs whose time has come.
func (q reads) DueBlobDeletions(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT blob_id FROM blob_gc WHERE not_before <= ? ORDER BY not_before, blob_id LIMIT ?`, ms(now), limit)
	if err != nil {
		return nil, fmt.Errorf("query blob deletions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Checkpoint runs a FULL WAL checkpoint, the durability barrier for
// deleting files. With synchronous=NORMAL a committed transaction is only
// guaranteed to survive a power loss once the WAL has been synced, which a
// checkpoint does before copying frames into the database file and syncing
// that too. A checkpoint blocked by readers or writers (beyond busy_timeout)
// returns ErrCheckpointBusy.
func (s *Store) Checkpoint(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	err := s.w.QueryRowContext(ctx, `PRAGMA wal_checkpoint(FULL)`).Scan(&busy, &logFrames, &checkpointed)
	if err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	if busy != 0 || checkpointed < logFrames {
		return fmt.Errorf("%w: %d of %d frames", ErrCheckpointBusy, checkpointed, logFrames)
	}
	return nil
}
