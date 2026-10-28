package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"go.xchunk.org/vela/internal/domain"
)

// trashCols mirrors objectCols with the object ID and path renamed, followed
// by the trash-specific columns.
const trashCols = `object_id, original_path, kind, target_id, blob_id, size, sha256, content_type, disposition, visibility,
	title, description, tags, created_at, updated_at, expires_at, revision, id, deleted_at, deleted_by_key, seq`

func scanTrash(sc scanner) (domain.TrashEntry, error) {
	var (
		e         domain.TrashEntry
		deletedAt int64
		deletedBy sql.NullString
	)
	o, err := scanObject(scannerFunc(func(dest ...any) error {
		return sc.Scan(append(dest, &e.ID, &deletedAt, &deletedBy, &e.Seq)...)
	}))
	if err != nil {
		return domain.TrashEntry{}, err
	}
	e.Object = o
	e.DeletedAt = fromMS(deletedAt)
	e.DeletedBy = deletedBy.String
	return e, nil
}

type scannerFunc func(dest ...any) error

func (f scannerFunc) Scan(dest ...any) error { return f(dest...) }

func (q reads) queryTrash(ctx context.Context, query string, args ...any) ([]domain.TrashEntry, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query trash: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.TrashEntry
	for rows.Next() {
		e, err := scanTrash(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// TrashByID returns a trash entry or domain.ErrNotFound.
func (q reads) TrashByID(ctx context.Context, id string) (domain.TrashEntry, error) {
	return scanTrash(q.db.QueryRowContext(ctx, `SELECT `+trashCols+` FROM trash WHERE id = ?`, id))
}

// ListTrash returns trash entries newest first. Entry IDs are ULIDs, so
// ordering by ID orders by deletion time and gives a stable cursor.
func (q reads) ListTrash(ctx context.Context, tq domain.TrashQuery) ([]domain.TrashEntry, error) {
	var conds []string
	var args []any
	for _, p := range []string{tq.Prefix, tq.Within} {
		if p != "" {
			conds = append(conds, "original_path >= ? AND original_path < ?")
			args = append(args, p, prefixUpper(p))
		}
	}
	if tq.Path != "" {
		conds = append(conds, "original_path = ?")
		args = append(args, tq.Path)
	}
	if tq.BeforeSeq > 0 {
		conds = append(conds, "seq < ?")
		args = append(args, tq.BeforeSeq)
	}
	query := `SELECT ` + trashCols + ` FROM trash`
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, " AND ")
	}
	query += ` ORDER BY seq DESC`
	if tq.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, tq.Limit)
	}
	return q.queryTrash(ctx, query, args...)
}

// TrashDeletedBefore returns up to limit entries deleted before cutoff,
// oldest first.
func (q reads) TrashDeletedBefore(ctx context.Context, cutoff time.Time, within string, limit int) ([]domain.TrashEntry, error) {
	if within == "" {
		return q.queryTrash(ctx, `SELECT `+trashCols+` FROM trash WHERE deleted_at < ? ORDER BY seq LIMIT ?`,
			ms(cutoff), limit)
	}
	return q.queryTrash(ctx, `SELECT `+trashCols+` FROM trash
		WHERE deleted_at < ? AND original_path >= ? AND original_path < ? ORDER BY seq LIMIT ?`,
		ms(cutoff), within, prefixUpper(within), limit)
}

// InsertTrash adds a trash entry.
func (t *Tx) InsertTrash(ctx context.Context, e domain.TrashEntry) error {
	args, err := objectArgs(&e.Object)
	if err != nil {
		return err
	}
	var by sql.NullString
	if e.DeletedBy != "" {
		by = sql.NullString{String: e.DeletedBy, Valid: true}
	}
	// The counter row is updated in the same transaction, which the single
	// writer serializes, so sequence numbers are strictly increasing.
	var seq int64
	err = t.tx.QueryRowContext(ctx, `INSERT INTO counters (name, value) VALUES ('trash', 1)
		ON CONFLICT (name) DO UPDATE SET value = value + 1 RETURNING value`).Scan(&seq)
	if err != nil {
		return fmt.Errorf("number trash entry: %w", err)
	}
	args = append(args, e.ID, ms(e.DeletedAt), by, seq)
	_, err = t.tx.ExecContext(ctx, `INSERT INTO trash (`+trashCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	if err != nil {
		return fmt.Errorf("insert trash entry: %w", mapErr(err))
	}
	return nil
}

// DeleteTrash removes a trash entry.
func (t *Tx) DeleteTrash(ctx context.Context, id string) error {
	res, err := t.tx.ExecContext(ctx, `DELETE FROM trash WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete trash entry: %w", err)
	}
	return expectOne(res, "delete trash entry")
}
