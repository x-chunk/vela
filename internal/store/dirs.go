package store

import (
	"context"
	"fmt"

	"go.xchunk.org/vela/api"
)

const dirPolicyColumns = "prefix, idx, hidden, noindex, title, description, created_at, updated_at, revision"

// DirPoliciesVersion changes only when a rule is written, including externally.
func (r reads) DirPoliciesVersion(ctx context.Context) (int64, error) {
	var v int64
	if err := r.db.QueryRowContext(ctx, `SELECT value FROM counters WHERE name = 'dir_policies'`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read directory policy generation: %w", err)
	}
	return v, nil
}

// DirPolicies returns the complete ordered rule set for the in-memory resolver.
func (r reads) DirPolicies(ctx context.Context) ([]api.DirPolicy, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+dirPolicyColumns+" FROM dir_policies ORDER BY prefix")
	if err != nil {
		return nil, fmt.Errorf("list directory policies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []api.DirPolicy{}
	for rows.Next() {
		p, err := scanDirPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DirPolicyByPrefix reads one rule without resolving inheritance.
func (r reads) DirPolicyByPrefix(ctx context.Context, prefix string) (api.DirPolicy, error) {
	return scanDirPolicy(r.db.QueryRowContext(ctx, "SELECT "+dirPolicyColumns+" FROM dir_policies WHERE prefix = ?", prefix))
}

func scanDirPolicy(row interface{ Scan(...any) error }) (api.DirPolicy, error) {
	var p api.DirPolicy
	var created, updated int64
	if err := row.Scan(&p.Prefix, &p.Index, &p.Hidden, &p.NoIndex, &p.Title, &p.Description, &created, &updated, &p.Revision); err != nil {
		return p, fmt.Errorf("read directory policy: %w", mapErr(err))
	}
	p.CreatedAt, p.UpdatedAt = fromMS(created), fromMS(updated)
	return p, nil
}

// PutDirPolicy stores a validated complete rule inside the caller's transaction.
func (t *Tx) PutDirPolicy(ctx context.Context, p api.DirPolicy) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO dir_policies (`+dirPolicyColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
 ON CONFLICT(prefix) DO UPDATE SET idx=excluded.idx, hidden=excluded.hidden, noindex=excluded.noindex,
 title=excluded.title, description=excluded.description, created_at=excluded.created_at,
 updated_at=excluded.updated_at, revision=excluded.revision`, p.Prefix, p.Index, p.Hidden, p.NoIndex, p.Title, p.Description, ms(p.CreatedAt), ms(p.UpdatedAt), p.Revision)
	if err != nil {
		return fmt.Errorf("write directory policy: %w", mapErr(err))
	}
	return nil
}

// DeleteDirPolicy deletes a rule; objects and other rules remain independent.
func (t *Tx) DeleteDirPolicy(ctx context.Context, prefix string) error {
	_, err := t.tx.ExecContext(ctx, `DELETE FROM dir_policies WHERE prefix = ?`, prefix)
	if err != nil {
		return fmt.Errorf("delete directory policy: %w", err)
	}
	return nil
}
