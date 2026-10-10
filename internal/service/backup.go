package service

import (
	"context"
	"errors"
)

// backupLock pins blobs: while any process holds it, the collector deletes
// nothing.
const backupLock = "backup"

// ErrBackupRunning is returned when another backup holds the lock.
var ErrBackupRunning = errors.New("another backup is running")

// HoldBackup guarantees that no blob file is deleted until release is
// called, so a database snapshot taken after it returns can be completed
// from the live blob directory. The returned context is canceled if the
// guarantee is lost (see holdLock); the backup must then fail.
//
// Blobs dropped from the database before the call may be deleted by a
// collector run that started earlier, but such blobs are not referenced by
// any later snapshot: once unreferenced, a blob never becomes referenced
// again.
func (s *Service) HoldBackup(ctx context.Context) (context.Context, func(), error) {
	// The lock row identifies the process, so a second backup in the same
	// process needs the mutex to be excluded.
	if !s.backupMu.TryLock() {
		return nil, nil, ErrBackupRunning
	}
	lock, ok, err := s.holdLock(ctx, backupLock)
	if err != nil || !ok {
		s.backupMu.Unlock()
		if err == nil {
			err = ErrBackupRunning
		}
		return nil, nil, err
	}
	return lock.ctx, func() {
		lock.release()
		s.backupMu.Unlock()
	}, nil
}
