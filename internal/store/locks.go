package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.xchunk.org/vela/internal/domain"
)

// Advisory locks live in the meta table as "lock:<name>" rows whose value is
// "<owner> <expiry ms>". They coordinate work across processes that share
// the database, such as the janitor in the server and "velad janitor run"
// started with docker exec. The expiry makes a lock left behind by a crashed
// process harmless.

// AcquireLock takes the named lock for owner until the given time, unless
// another owner holds an unexpired lock. Re-acquiring an own lock extends it.
func (t *Tx) AcquireLock(ctx context.Context, name, owner string, now, until time.Time) (bool, error) {
	key := "lock:" + name
	var value string
	err := t.tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if err == nil {
		var holder string
		var expiry int64
		if _, scanErr := fmt.Sscanf(value, "%s %d", &holder, &expiry); scanErr == nil && holder != owner && expiry > ms(now) {
			return false, nil
		}
	} else if !errors.Is(mapErr(err), domain.ErrNotFound) {
		return false, fmt.Errorf("read lock: %w", err)
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, fmt.Sprintf("%s %d", owner, ms(until)))
	if err != nil {
		return false, fmt.Errorf("write lock: %w", err)
	}
	return true, nil
}

// ReleaseLock drops the named lock if owner holds it.
func (t *Tx) ReleaseLock(ctx context.Context, name, owner string) error {
	key := "lock:" + name
	var value string
	err := t.tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(mapErr(err), domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	var holder string
	var expiry int64
	if _, err := fmt.Sscanf(value, "%s %d", &holder, &expiry); err != nil || holder != owner {
		return nil
	}
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, key); err != nil {
		return fmt.Errorf("release lock: %w", err)
	}
	return nil
}
