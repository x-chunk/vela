package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

const objectCols = `id, path, blob_id, size, sha256, content_type, disposition, visibility,
	title, description, tags, created_at, updated_at, expires_at, revision`

type scanner interface {
	Scan(dest ...any) error
}

func scanObject(sc scanner) (domain.Object, error) {
	var (
		o                domain.Object
		disp, vis, tags  string
		created, updated int64
		expires          sql.NullInt64
	)
	err := sc.Scan(&o.ID, &o.Path, &o.BlobID, &o.Size, &o.SHA256, &o.ContentType, &disp, &vis,
		&o.Title, &o.Description, &tags, &created, &updated, &expires, &o.Revision)
	if err != nil {
		return domain.Object{}, mapErr(err)
	}
	o.Disposition = api.Disposition(disp)
	o.Visibility = api.Visibility(vis)
	if err := json.Unmarshal([]byte(tags), &o.Tags); err != nil {
		return domain.Object{}, fmt.Errorf("object %s: corrupt tags: %w", o.ID, err)
	}
	if o.Tags == nil {
		o.Tags = []string{}
	}
	o.CreatedAt = fromMS(created)
	o.UpdatedAt = fromMS(updated)
	o.ExpiresAt = fromNullMS(expires)
	return o, nil
}

func objectArgs(o *domain.Object) ([]any, error) {
	tags := o.Tags
	if tags == nil {
		tags = []string{}
	}
	tagJSON, err := json.Marshal(tags)
	if err != nil {
		return nil, fmt.Errorf("encode tags: %w", err)
	}
	return []any{o.ID, o.Path, o.BlobID, o.Size, o.SHA256, o.ContentType, string(o.Disposition),
		string(o.Visibility), o.Title, o.Description, string(tagJSON), ms(o.CreatedAt), ms(o.UpdatedAt),
		nullMS(o.ExpiresAt), o.Revision}, nil
}

func (q reads) queryObjects(ctx context.Context, query string, args ...any) ([]domain.Object, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query objects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Object
	for rows.Next() {
		o, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ObjectByPath returns the object at path or domain.ErrNotFound.
func (q reads) ObjectByPath(ctx context.Context, path string) (domain.Object, error) {
	return scanObject(q.db.QueryRowContext(ctx, `SELECT `+objectCols+` FROM objects WHERE path = ?`, path))
}

// ObjectByID returns the object with id or domain.ErrNotFound.
func (q reads) ObjectByID(ctx context.Context, id string) (domain.Object, error) {
	return scanObject(q.db.QueryRowContext(ctx, `SELECT `+objectCols+` FROM objects WHERE id = ?`, id))
}

// FirstExisting returns the first of paths that holds an object (in byte
// order), or "" if none does. Used for the file/directory conflict check.
func (q reads) FirstExisting(ctx context.Context, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	args := make([]any, len(paths))
	for i, p := range paths {
		args[i] = p
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(paths)), ",")
	var p string
	err := q.db.QueryRowContext(ctx, `SELECT path FROM objects WHERE path IN (`+marks+`) ORDER BY path LIMIT 1`, args...).Scan(&p)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("check paths: %w", err)
	}
	return p, nil
}

// FirstUnder returns the first object path below prefix, or "" if the
// directory is empty.
func (q reads) FirstUnder(ctx context.Context, prefix string) (string, error) {
	var p string
	err := q.db.QueryRowContext(ctx, `SELECT path FROM objects WHERE path >= ? AND path < ? ORDER BY path LIMIT 1`,
		prefix, prefixUpper(prefix)).Scan(&p)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("check prefix: %w", err)
	}
	return p, nil
}

// ObjectsUnder returns every object below prefix in path order.
func (q reads) ObjectsUnder(ctx context.Context, prefix string) ([]domain.Object, error) {
	if prefix == "" {
		return q.queryObjects(ctx, `SELECT `+objectCols+` FROM objects ORDER BY path`)
	}
	return q.queryObjects(ctx, `SELECT `+objectCols+` FROM objects WHERE path >= ? AND path < ? ORDER BY path`,
		prefix, prefixUpper(prefix))
}

// ExpiredObjects returns up to limit objects whose expires_at is before now.
func (q reads) ExpiredObjects(ctx context.Context, now time.Time, limit int) ([]domain.Object, error) {
	return q.queryObjects(ctx, `SELECT `+objectCols+` FROM objects
		WHERE expires_at IS NOT NULL AND expires_at < ? ORDER BY expires_at LIMIT ?`, ms(now), limit)
}

// HasPublicObjects checks public index existence without loading metadata or
// counting the entire subtree. Only a matching live row is needed.
func (q reads) HasPublicObjects(ctx context.Context, prefix string, now time.Time) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM objects WHERE visibility = 'public'
  AND (expires_at IS NULL OR expires_at >= ?)`
	args := []any{ms(now)}
	if prefix != "" {
		query += " AND path >= ? AND path < ?"
		args = append(args, prefix, prefixUpper(prefix))
	}
	query += " LIMIT 1)"
	var exists bool
	if err := q.db.QueryRowContext(ctx, query, args...).Scan(&exists); err != nil {
		return false, fmt.Errorf("check public directory existence: %w", err)
	}
	return exists, nil
}

// DirStats counts public, unexpired objects below prefix and returns the
// latest update time among them. Policy listings use it to find orphaned rules.
func (q reads) DirStats(ctx context.Context, prefix string, now time.Time) (int, time.Time, error) {
	var (
		n      int
		latest sql.NullInt64
	)
	query := `SELECT COUNT(*), MAX(updated_at) FROM objects
  WHERE visibility = 'public' AND (expires_at IS NULL OR expires_at >= ?)`
	args := []any{ms(now)}
	if prefix != "" {
		query += " AND path >= ? AND path < ?"
		args = append(args, prefix, prefixUpper(prefix))
	}
	err := q.db.QueryRowContext(ctx, query, args...).Scan(&n, &latest)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("directory stats: %w", err)
	}
	if !latest.Valid {
		return n, time.Time{}, nil
	}
	return n, fromMS(latest.Int64), nil
}

// BlobReferenced reports whether a live or trashed object uses blobID.
func (q reads) BlobReferenced(ctx context.Context, blobID string) (bool, error) {
	var one int
	err := q.db.QueryRowContext(ctx, `SELECT 1 FROM objects WHERE blob_id = ?
		UNION ALL SELECT 1 FROM trash WHERE blob_id = ? LIMIT 1`, blobID, blobID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check blob reference: %w", err)
	}
	return true, nil
}

// ListObjects returns one page of a listing.
//
// Recursive listings are a plain range scan. Non-recursive listings must also
// produce sub-directories, which are not stored anywhere: a directory exists
// while some object path continues past it. The loop walks objects in path
// order; on meeting "prefix/sub/...", it emits "prefix/sub/" and jumps past
// everything under it with a new range query, so each directory costs one
// query regardless of how many objects it holds.
func (q reads) ListObjects(ctx context.Context, lq domain.ListQuery) (domain.ListResult, error) {
	scope := lq.Prefix
	if len(lq.Within) > len(scope) {
		scope = lq.Within
	}
	var where []string
	var filterArgs []any
	if upper := prefixUpper(scope); upper != "" {
		where = append(where, "path < ?")
		filterArgs = append(filterArgs, upper)
	}
	if len(lq.Visibilities) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(lq.Visibilities)), ",")
		where = append(where, "visibility IN ("+marks+")")
		for _, v := range lq.Visibilities {
			filterArgs = append(filterArgs, string(v))
		}
	}
	if !lq.LiveAt.IsZero() {
		where = append(where, "(expires_at IS NULL OR expires_at >= ?)")
		filterArgs = append(filterArgs, ms(lq.LiveAt))
	}

	// start is the lower bound for the next query; inclusive tells whether
	// the bound itself may be returned.
	start, inclusive := scope, true
	if lq.After != "" && lq.After >= scope {
		if strings.HasSuffix(lq.After, "/") {
			start, inclusive = prefixUpper(lq.After), true
		} else {
			start, inclusive = lq.After, false
		}
	}

	want := lq.Limit + 1 // one extra entry tells whether there is a next page
	if lq.Limit <= 0 {
		want = 0
	}
	var res domain.ListResult
	count := 0
	full := func() bool { return want > 0 && count >= want }
	var lastKeys []string

	for !full() {
		op := ">"
		if inclusive {
			op = ">="
		}
		conds := append([]string{"path " + op + " ?"}, where...)
		args := append([]any{start}, filterArgs...)
		batch := 256
		if want > 0 && want-count < batch {
			batch = want - count
		}
		query := `SELECT ` + objectCols + ` FROM objects WHERE ` + strings.Join(conds, " AND ") + ` ORDER BY path LIMIT ?`
		objs, err := q.queryObjects(ctx, query, append(args, batch)...)
		if err != nil {
			return domain.ListResult{}, err
		}
		if len(objs) == 0 {
			break
		}
		jumped := false
		for _, o := range objs {
			if !lq.Recursive {
				rest := o.Path[len(lq.Prefix):]
				if i := strings.IndexByte(rest, '/'); i >= 0 {
					dir := lq.Prefix + rest[:i+1]
					res.Dirs = append(res.Dirs, dir)
					lastKeys = append(lastKeys, dir)
					count++
					start, inclusive = prefixUpper(dir), true
					jumped = true
					break
				}
			}
			res.Objects = append(res.Objects, o)
			lastKeys = append(lastKeys, o.Path)
			count++
			start, inclusive = o.Path, false
			if full() {
				break
			}
		}
		if !jumped && len(objs) < batch {
			break
		}
	}

	if want > 0 && count >= want {
		// Drop the look-ahead entry.
		res.More = true
		extra := lastKeys[len(lastKeys)-1]
		lastKeys = lastKeys[:len(lastKeys)-1]
		if strings.HasSuffix(extra, "/") {
			res.Dirs = res.Dirs[:len(res.Dirs)-1]
		} else {
			res.Objects = res.Objects[:len(res.Objects)-1]
		}
	}
	if len(lastKeys) > 0 {
		res.Last = lastKeys[len(lastKeys)-1]
	}
	return res, nil
}

// InsertObject adds a new object row.
func (t *Tx) InsertObject(ctx context.Context, o domain.Object) error {
	args, err := objectArgs(&o)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO objects (`+objectCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	if err != nil {
		return fmt.Errorf("insert object: %w", mapErr(err))
	}
	return nil
}

// UpdateObject overwrites every column of the object with o.ID.
func (t *Tx) UpdateObject(ctx context.Context, o domain.Object) error {
	args, err := objectArgs(&o)
	if err != nil {
		return err
	}
	res, err := t.tx.ExecContext(ctx, `UPDATE objects SET id = ?, path = ?, blob_id = ?, size = ?, sha256 = ?,
		content_type = ?, disposition = ?, visibility = ?, title = ?, description = ?, tags = ?,
		created_at = ?, updated_at = ?, expires_at = ?, revision = ? WHERE id = ?`, append(args, o.ID)...)
	if err != nil {
		return fmt.Errorf("update object: %w", mapErr(err))
	}
	return expectOne(res, "update object")
}

// DeleteObject removes the object row with id.
func (t *Tx) DeleteObject(ctx context.Context, id string) error {
	res, err := t.tx.ExecContext(ctx, `DELETE FROM objects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return expectOne(res, "delete object")
}

func expectOne(res sql.Result, op string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
