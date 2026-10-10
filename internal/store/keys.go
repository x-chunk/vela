package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

// Signing keys.

func scanSigningKey(sc scanner) (domain.SigningKey, error) {
	var (
		k       domain.SigningKey
		created int64
		retired sql.NullInt64
	)
	if err := sc.Scan(&k.ID, &k.Secret, &created, &retired); err != nil {
		return domain.SigningKey{}, mapErr(err)
	}
	k.CreatedAt = fromMS(created)
	k.RetiredAt = fromNullMS(retired)
	return k, nil
}

// ActiveSigningKey returns the newest unretired signing key.
func (q reads) ActiveSigningKey(ctx context.Context) (domain.SigningKey, error) {
	return scanSigningKey(q.db.QueryRowContext(ctx, `SELECT id, secret, created_at, retired_at FROM signing_keys
		WHERE retired_at IS NULL ORDER BY created_at DESC, id DESC LIMIT 1`))
}

// SigningKeys returns all signing keys, newest first.
func (q reads) SigningKeys(ctx context.Context) ([]domain.SigningKey, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, secret, created_at, retired_at FROM signing_keys ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query signing keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.SigningKey
	for rows.Next() {
		k, err := scanSigningKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// InsertSigningKey adds a signing key.
func (t *Tx) InsertSigningKey(ctx context.Context, k domain.SigningKey) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO signing_keys (id, secret, created_at, retired_at) VALUES (?,?,?,?)`,
		k.ID, k.Secret, ms(k.CreatedAt), nullMS(k.RetiredAt))
	if err != nil {
		return fmt.Errorf("insert signing key: %w", mapErr(err))
	}
	return nil
}

// RetireSigningKeys retires every active signing key and returns their IDs.
func (t *Tx) RetireSigningKeys(ctx context.Context, at time.Time) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, `UPDATE signing_keys SET retired_at = ? WHERE retired_at IS NULL RETURNING id`, ms(at))
	if err != nil {
		return nil, fmt.Errorf("retire signing keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteSigningKey destroys a signing key; links it signed stop verifying.
func (t *Tx) DeleteSigningKey(ctx context.Context, id string) error {
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM signing_keys WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete signing key: %w", err)
	}
	return nil
}

// DeleteUnusedRetiredSigningKeys removes retired keys that no live link
// (unrevoked and unexpired at now) depends on.
func (t *Tx) DeleteUnusedRetiredSigningKeys(ctx context.Context, now time.Time) (int, error) {
	res, err := t.tx.ExecContext(ctx, `DELETE FROM signing_keys WHERE retired_at IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM links l WHERE l.signing_key_id = signing_keys.id AND l.revoked_at IS NULL AND l.expires_at >= ?)`, ms(now))
	if err != nil {
		return 0, fmt.Errorf("delete retired signing keys: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// API keys.

const apiKeyCols = `id, name, secret_hash, scope, prefix, created_at, last_used_at, revoked_at`

func scanAPIKey(sc scanner) (domain.APIKey, error) {
	var (
		k               domain.APIKey
		scope           string
		created         int64
		lastUsed, revok sql.NullInt64
	)
	if err := sc.Scan(&k.ID, &k.Name, &k.SecretHash, &scope, &k.Prefix, &created, &lastUsed, &revok); err != nil {
		return domain.APIKey{}, mapErr(err)
	}
	k.Scope = api.Scope(scope)
	k.CreatedAt = fromMS(created)
	k.LastUsedAt = fromNullMS(lastUsed)
	k.RevokedAt = fromNullMS(revok)
	return k, nil
}

// APIKeyByID returns an API key, revoked or not.
func (q reads) APIKeyByID(ctx context.Context, id string) (domain.APIKey, error) {
	return scanAPIKey(q.db.QueryRowContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE id = ?`, id))
}

// ListAPIKeys returns all API keys ordered by creation.
func (q reads) ListAPIKeys(ctx context.Context) ([]domain.APIKey, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("query api keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// InsertAPIKey adds an API key. A duplicate ID or active name yields
// domain.ErrDuplicate.
func (t *Tx) InsertAPIKey(ctx context.Context, k domain.APIKey) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO api_keys (`+apiKeyCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		k.ID, k.Name, k.SecretHash, string(k.Scope), k.Prefix, ms(k.CreatedAt), nullMS(k.LastUsedAt), nullMS(k.RevokedAt))
	if err != nil {
		return fmt.Errorf("insert api key: %w", mapErr(err))
	}
	return nil
}

// RevokeAPIKey revokes a key; revoking twice keeps the first timestamp.
func (t *Tx) RevokeAPIKey(ctx context.Context, id string, at time.Time) error {
	res, err := t.tx.ExecContext(ctx, `UPDATE api_keys SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, ms(at), id)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	return expectOne(res, "revoke api key")
}

// TouchAPIKey records the last use of a key.
func (t *Tx) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	if _, err := t.tx.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, ms(at), id); err != nil {
		return fmt.Errorf("touch api key: %w", err)
	}
	return nil
}
