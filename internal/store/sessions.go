package store

import (
	"context"
	"fmt"
	"time"

	"go.xchunk.org/vela/internal/domain"
)

// Web sessions.

const webSessionCols = `id_hash, key_id, created_at, last_seen_at, expires_at`

func scanWebSession(sc scanner) (domain.WebSession, error) {
	var (
		s                      domain.WebSession
		created, seen, expires int64
	)
	if err := sc.Scan(&s.IDHash, &s.KeyID, &created, &seen, &expires); err != nil {
		return domain.WebSession{}, mapErr(err)
	}
	s.CreatedAt = fromMS(created)
	s.LastSeenAt = fromMS(seen)
	s.ExpiresAt = fromMS(expires)
	return s, nil
}

// WebSessionByHash returns the session whose ID hashes to idHash.
func (q reads) WebSessionByHash(ctx context.Context, idHash []byte) (domain.WebSession, error) {
	return scanWebSession(q.db.QueryRowContext(ctx, `SELECT `+webSessionCols+` FROM web_sessions WHERE id_hash = ?`, idHash))
}

// InsertWebSession adds a session. A duplicate ID hash yields
// domain.ErrDuplicate.
func (t *Tx) InsertWebSession(ctx context.Context, s domain.WebSession) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO web_sessions (`+webSessionCols+`) VALUES (?,?,?,?,?)`,
		s.IDHash, s.KeyID, ms(s.CreatedAt), ms(s.LastSeenAt), ms(s.ExpiresAt))
	if err != nil {
		return fmt.Errorf("insert web session: %w", mapErr(err))
	}
	return nil
}

// TouchWebSession records activity. The time only moves forward, so a slow
// request cannot shorten the idle timeout set by a newer one.
func (t *Tx) TouchWebSession(ctx context.Context, idHash []byte, at time.Time) error {
	_, err := t.tx.ExecContext(ctx, `UPDATE web_sessions SET last_seen_at = ? WHERE id_hash = ? AND last_seen_at < ?`,
		ms(at), idHash, ms(at))
	if err != nil {
		return fmt.Errorf("touch web session: %w", err)
	}
	return nil
}

// DeleteWebSession removes one session; an absent session is not an error,
// because signing out twice must succeed.
func (t *Tx) DeleteWebSession(ctx context.Context, idHash []byte) error {
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE id_hash = ?`, idHash); err != nil {
		return fmt.Errorf("delete web session: %w", err)
	}
	return nil
}

// DeleteWebSessions removes every session and returns how many existed.
func (t *Tx) DeleteWebSessions(ctx context.Context) (int, error) {
	res, err := t.tx.ExecContext(ctx, `DELETE FROM web_sessions`)
	if err != nil {
		return 0, fmt.Errorf("delete web sessions: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// DeleteStaleWebSessions removes sessions past their absolute expiry at now,
// sessions last seen before idleCutoff, and sessions whose key is revoked or
// missing. Such rows can never authenticate again.
func (t *Tx) DeleteStaleWebSessions(ctx context.Context, now, idleCutoff time.Time) (int, error) {
	res, err := t.tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE expires_at < ? OR last_seen_at < ?
		OR NOT EXISTS (SELECT 1 FROM api_keys k WHERE k.id = web_sessions.key_id AND k.revoked_at IS NULL)`,
		ms(now), ms(idleCutoff))
	if err != nil {
		return 0, fmt.Errorf("delete stale web sessions: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}
