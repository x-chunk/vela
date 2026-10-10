package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"go.xchunk.org/vela/internal/domain"
)

// linkSelect joins a link with its signing key and with wherever its object
// currently lives. An object is in at most one of objects and trash (trash
// object_id is unique and rows move between the tables), so the joins never
// multiply rows.
const linkSelect = `SELECT l.id, l.object_id, l.signing_key_id, l.filename, l.note, l.created_at,
	l.expires_at, l.revoked_at, l.created_by_key, k.secret, o.path, t.original_path
	FROM links l
	LEFT JOIN signing_keys k ON k.id = l.signing_key_id
	LEFT JOIN objects o ON o.id = l.object_id
	LEFT JOIN trash t ON t.object_id = l.object_id`

func scanLink(sc scanner) (domain.LinkRecord, error) {
	var (
		r                   domain.LinkRecord
		created, expires    int64
		revoked             sql.NullInt64
		createdBy           sql.NullString
		livePath, trashPath sql.NullString
	)
	err := sc.Scan(&r.ID, &r.ObjectID, &r.SigningKeyID, &r.Filename, &r.Note, &created, &expires,
		&revoked, &createdBy, &r.KeySecret, &livePath, &trashPath)
	if err != nil {
		return domain.LinkRecord{}, mapErr(err)
	}
	r.CreatedAt = fromMS(created)
	r.ExpiresAt = fromMS(expires)
	r.RevokedAt = fromNullMS(revoked)
	r.CreatedBy = createdBy.String
	switch {
	case livePath.Valid:
		r.ObjectState, r.ObjectPath = domain.ObjectLive, livePath.String
	case trashPath.Valid:
		r.ObjectState, r.ObjectPath = domain.ObjectTrashed, trashPath.String
	default:
		r.ObjectState = domain.ObjectGone
	}
	return r, nil
}

// LinkByID returns a link joined with its key and object location.
func (q reads) LinkByID(ctx context.Context, id string) (domain.LinkRecord, error) {
	return scanLink(q.db.QueryRowContext(ctx, linkSelect+` WHERE l.id = ?`, id))
}

// ListLinks returns links matching lq, newest first.
func (q reads) ListLinks(ctx context.Context, lq domain.LinkQuery) ([]domain.LinkRecord, error) {
	var conds []string
	var args []any
	if lq.ObjectID != "" {
		conds = append(conds, "l.object_id = ?")
		args = append(args, lq.ObjectID)
	}
	if lq.Within != "" {
		conds = append(conds, "COALESCE(o.path, t.original_path) >= ? AND COALESCE(o.path, t.original_path) < ?")
		args = append(args, lq.Within, prefixUpper(lq.Within))
	}
	if !lq.IncludeExpired {
		conds = append(conds, "l.expires_at >= ?")
		args = append(args, ms(lq.Now))
	}
	if !lq.IncludeRevoked {
		conds = append(conds, "l.revoked_at IS NULL")
	}
	query := linkSelect
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, " AND ")
	}
	query += ` ORDER BY l.id DESC`
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.LinkRecord
	for rows.Next() {
		r, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertLink adds a link record.
func (t *Tx) InsertLink(ctx context.Context, l domain.Link) error {
	var by sql.NullString
	if l.CreatedBy != "" {
		by = sql.NullString{String: l.CreatedBy, Valid: true}
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO links (id, object_id, signing_key_id, filename, note,
		created_at, expires_at, revoked_at, created_by_key) VALUES (?,?,?,?,?,?,?,?,?)`,
		l.ID, l.ObjectID, l.SigningKeyID, l.Filename, l.Note, ms(l.CreatedAt), ms(l.ExpiresAt),
		nullMS(l.RevokedAt), by)
	if err != nil {
		return fmt.Errorf("insert link: %w", mapErr(err))
	}
	return nil
}

// RevokeLink marks a link revoked. Revoking an already revoked link keeps the
// original revocation time.
func (t *Tx) RevokeLink(ctx context.Context, id string, at time.Time) error {
	res, err := t.tx.ExecContext(ctx, `UPDATE links SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, ms(at), id)
	if err != nil {
		return fmt.Errorf("revoke link: %w", err)
	}
	return expectOne(res, "revoke link")
}

// RevokeLinksBySigningKey revokes every unrevoked link signed with keyID.
func (t *Tx) RevokeLinksBySigningKey(ctx context.Context, keyID string, at time.Time) (int, error) {
	res, err := t.tx.ExecContext(ctx, `UPDATE links SET revoked_at = ? WHERE signing_key_id = ? AND revoked_at IS NULL`, ms(at), keyID)
	if err != nil {
		return 0, fmt.Errorf("revoke links: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// DeleteLinksByObject removes every link of an object (used on purge).
func (t *Tx) DeleteLinksByObject(ctx context.Context, objectID string) (int, error) {
	res, err := t.tx.ExecContext(ctx, `DELETE FROM links WHERE object_id = ?`, objectID)
	if err != nil {
		return 0, fmt.Errorf("delete links: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// DeleteLinksBefore removes links that expired, or were revoked, before
// cutoff.
func (t *Tx) DeleteLinksBefore(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := t.tx.ExecContext(ctx, `DELETE FROM links WHERE expires_at < ? OR revoked_at < ?`, ms(cutoff), ms(cutoff))
	if err != nil {
		return 0, fmt.Errorf("delete old links: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}
