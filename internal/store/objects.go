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

// objectCols are the columns of an objects row, in the order of objectArgs.
const objectCols = `id, path, kind, target_id, blob_id, size, sha256, content_type, disposition, visibility,
	title, description, tags, created_at, updated_at, expires_at, revision`

// Every read of an object also resolves its symlink target in the same
// statement: the live target (t) and, failing that, the target's trash entry
// (tt). One statement gives one consistent snapshot, so a concurrent rename,
// trash or overwrite of the target cannot be observed half-way.
const (
	targetCols = `t.id, t.path, t.blob_id, t.size, t.sha256, t.content_type, t.visibility, t.expires_at, tt.original_path`
	targetJoin = ` LEFT JOIN objects t ON t.id = %[1]s.target_id LEFT JOIN trash tt ON tt.object_id = %[1]s.target_id`
)

// objectSelect selects objects as "o" together with their targets.
var objectSelect = `SELECT ` + qualify(objectCols, "o") + `, ` + targetCols + ` FROM objects o` + fmt.Sprintf(targetJoin, "o")

// SQL expressions over "o" and its target "t". A symlink takes the stricter
// of its own and its target's visibility, and is private while broken (see
// domain.Object.EffectiveVisibility).
const (
	effectiveVisibilitySQL = `(CASE WHEN o.kind = 'file' THEN o.visibility
		WHEN t.id IS NULL OR o.visibility = 'private' OR t.visibility = 'private' THEN 'private'
		WHEN o.visibility = 'unlisted' OR t.visibility = 'unlisted' THEN 'unlisted'
		ELSE 'public' END)`
	// liveSQL takes now twice: an object is live when it has not expired
	// and, for a symlink, its target is live and has not expired either.
	liveSQL   = `(o.expires_at IS NULL OR o.expires_at >= ?) AND (o.kind = 'file' OR (t.id IS NOT NULL AND (t.expires_at IS NULL OR t.expires_at >= ?)))`
	brokenSQL = `o.kind = 'symlink' AND t.id IS NULL`
)

// qualify prefixes every column of a comma-separated list with alias.
func qualify(cols, alias string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

type scanner interface {
	Scan(dest ...any) error
}

// scanObject reads the columns of objectCols followed by targetCols.
func scanObject(sc scanner) (domain.Object, error) {
	var (
		o                domain.Object
		kind             string
		target, blobID   sql.NullString
		disp, vis, tags  string
		created, updated int64
		expires          sql.NullInt64

		tID, tPath, tBlob, tSHA, tType, tVis, ttPath sql.NullString
		tSize, tExpires                              sql.NullInt64
	)
	err := sc.Scan(&o.ID, &o.Path, &kind, &target, &blobID, &o.Size, &o.SHA256, &o.ContentType, &disp, &vis,
		&o.Title, &o.Description, &tags, &created, &updated, &expires, &o.Revision,
		&tID, &tPath, &tBlob, &tSize, &tSHA, &tType, &tVis, &tExpires, &ttPath)
	if err != nil {
		return domain.Object{}, mapErr(err)
	}
	o.Kind, o.TargetID, o.BlobID = domain.Kind(kind), target.String, blobID.String
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
	if o.IsSymlink() {
		t := &domain.Target{ID: o.TargetID, State: domain.ObjectGone}
		switch {
		case tID.Valid:
			t.State, t.Path, t.BlobID = domain.ObjectLive, tPath.String, tBlob.String
			t.Visibility, t.ExpiresAt = api.Visibility(tVis.String), fromNullMS(tExpires)
			o.Size, o.SHA256, o.ContentType = tSize.Int64, tSHA.String, tType.String
		case ttPath.Valid:
			t.State, t.Path = domain.ObjectTrashed, ttPath.String
		}
		o.Target = t
	}
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
	kind := o.Kind
	if kind == "" {
		kind = domain.KindFile
	}
	var target, blobID sql.NullString
	size, sum, contentType := o.Size, o.SHA256, o.ContentType
	if kind == domain.KindSymlink {
		// A symlink owns no content; what readers see comes from the target,
		// so nothing derived from it may be persisted on the symlink row.
		target = sql.NullString{String: o.TargetID, Valid: true}
		size, sum, contentType = 0, "", ""
	} else {
		blobID = sql.NullString{String: o.BlobID, Valid: true}
	}
	return []any{o.ID, o.Path, string(kind), target, blobID, size, sum, contentType, string(o.Disposition),
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
	return scanObject(q.db.QueryRowContext(ctx, objectSelect+` WHERE o.path = ?`, path))
}

// ObjectByID returns the object with id or domain.ErrNotFound.
func (q reads) ObjectByID(ctx context.Context, id string) (domain.Object, error) {
	return scanObject(q.db.QueryRowContext(ctx, objectSelect+` WHERE o.id = ?`, id))
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
		return q.queryObjects(ctx, objectSelect+` ORDER BY o.path`)
	}
	return q.queryObjects(ctx, objectSelect+` WHERE o.path >= ? AND o.path < ? ORDER BY o.path`,
		prefix, prefixUpper(prefix))
}

// ExpiredObjects returns up to limit objects whose expires_at is before now.
func (q reads) ExpiredObjects(ctx context.Context, now time.Time, limit int) ([]domain.Object, error) {
	return q.queryObjects(ctx, objectSelect+`
		WHERE o.expires_at IS NOT NULL AND o.expires_at < ? ORDER BY o.expires_at LIMIT ?`, ms(now), limit)
}

// HasPublicObjects checks public index existence without loading metadata or
// counting the entire subtree. Only a matching live row is needed; symlinks
// count by their effective visibility and their target.
func (q reads) HasPublicObjects(ctx context.Context, prefix string, now time.Time) (bool, error) {
	exists, err := q.hasLiveObjects(ctx, prefix, now, true)
	if err != nil {
		return false, fmt.Errorf("check public directory existence: %w", err)
	}
	return exists, nil
}

// HasLiveObjects reports whether any unexpired object, whatever its
// visibility, lies below prefix. Signed-in web views use it to decide whether
// a key's subtree can be entered.
func (q reads) HasLiveObjects(ctx context.Context, prefix string, now time.Time) (bool, error) {
	exists, err := q.hasLiveObjects(ctx, prefix, now, false)
	if err != nil {
		return false, fmt.Errorf("check directory existence: %w", err)
	}
	return exists, nil
}

func (q reads) hasLiveObjects(ctx context.Context, prefix string, now time.Time, publicOnly bool) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM objects o` + fmt.Sprintf(targetJoin, "o") + ` WHERE ` + liveSQL
	args := []any{ms(now), ms(now)}
	if publicOnly {
		query += " AND " + effectiveVisibilitySQL + " = 'public'"
	}
	if prefix != "" {
		query += " AND o.path >= ? AND o.path < ?"
		args = append(args, prefix, prefixUpper(prefix))
	}
	query += " LIMIT 1)"
	var exists bool
	err := q.db.QueryRowContext(ctx, query, args...).Scan(&exists)
	return exists, err
}

// DirStats counts public, live objects below prefix and returns the
// latest update time among them. Policy listings use it to find orphaned rules.
func (q reads) DirStats(ctx context.Context, prefix string, now time.Time) (int, time.Time, error) {
	var (
		n      int
		latest sql.NullInt64
	)
	query := `SELECT COUNT(*), MAX(o.updated_at) FROM objects o` + fmt.Sprintf(targetJoin, "o") +
		` WHERE ` + effectiveVisibilitySQL + ` = 'public' AND ` + liveSQL
	args := []any{ms(now), ms(now)}
	if prefix != "" {
		query += " AND o.path >= ? AND o.path < ?"
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
// Symlinks have a NULL blob_id, which never equals anything.
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
		where = append(where, "o.path < ?")
		filterArgs = append(filterArgs, upper)
	}
	if len(lq.Visibilities) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(lq.Visibilities)), ",")
		where = append(where, effectiveVisibilitySQL+" IN ("+marks+")")
		for _, v := range lq.Visibilities {
			filterArgs = append(filterArgs, string(v))
		}
	}
	if !lq.LiveAt.IsZero() {
		where = append(where, liveSQL)
		filterArgs = append(filterArgs, ms(lq.LiveAt), ms(lq.LiveAt))
	}
	if lq.Kind != "" {
		where = append(where, "o.kind = ?")
		filterArgs = append(filterArgs, string(lq.Kind))
	}
	if lq.Broken {
		where = append(where, brokenSQL)
	}
	if lq.TargetID != "" {
		where = append(where, "o.target_id = ?")
		filterArgs = append(filterArgs, lq.TargetID)
	}
	if lq.TargetWithin != "" {
		where = append(where, "(o.kind = 'file' OR "+effectiveVisibilitySQL+" <> 'private' OR (t.path >= ? AND t.path < ?))")
		filterArgs = append(filterArgs, lq.TargetWithin, prefixUpper(lq.TargetWithin))
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
		conds := append([]string{"o.path " + op + " ?"}, where...)
		args := append([]any{start}, filterArgs...)
		batch := 256
		if want > 0 && want-count < batch {
			batch = want - count
		}
		query := objectSelect + ` WHERE ` + strings.Join(conds, " AND ") + ` ORDER BY o.path LIMIT ?`
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

// CountSymlinksTo counts the symlinks below within ("" for all) that refer
// to any of targetIDs.
func (q reads) CountSymlinksTo(ctx context.Context, targetIDs []string, within string) (int, error) {
	const chunk = 500 // stay well below SQLite's limit on bound parameters
	total := 0
	for len(targetIDs) > 0 {
		n := min(chunk, len(targetIDs))
		args := make([]any, 0, n+2)
		for _, id := range targetIDs[:n] {
			args = append(args, id)
		}
		targetIDs = targetIDs[n:]
		query := `SELECT COUNT(*) FROM objects WHERE target_id IN (` + strings.TrimSuffix(strings.Repeat("?,", n), ",") + `)`
		if within != "" {
			query += ` AND path >= ? AND path < ?`
			args = append(args, within, prefixUpper(within))
		}
		var c int
		if err := q.db.QueryRowContext(ctx, query, args...).Scan(&c); err != nil {
			return 0, fmt.Errorf("count symlinks: %w", err)
		}
		total += c
	}
	return total, nil
}

// InsertObject adds a new object row.
func (t *Tx) InsertObject(ctx context.Context, o domain.Object) error {
	args, err := objectArgs(&o)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO objects (`+objectCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
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
	res, err := t.tx.ExecContext(ctx, `UPDATE objects SET id = ?, path = ?, kind = ?, target_id = ?, blob_id = ?, size = ?, sha256 = ?,
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
