package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"go.xchunk.org/vela/api"
)

// DirListOptions selects a page of directory policies.
type DirListOptions struct {
	Prefix    string
	Recursive bool
	Orphaned  bool
	Limit     int
	Cursor    string
}

// ListDirPolicies returns stored rules, restricted to the key's prefix.
func (c *Client) ListDirPolicies(ctx context.Context, opts DirListOptions) (*api.DirPolicyList, error) {
	q := url.Values{"prefix": {opts.Prefix}, "recursive": {strconv.FormatBool(opts.Recursive)}, "orphaned": {strconv.FormatBool(opts.Orphaned)}}
	if opts.Limit != 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	var out api.DirPolicyList
	if err := c.call(ctx, request{method: http.MethodGet, route: api.RouteDirs, query: q, idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListAllDirPolicies follows pagination while retaining the selected filters.
func (c *Client) ListAllDirPolicies(ctx context.Context, opts DirListOptions) (*api.DirPolicyList, error) {
	out := &api.DirPolicyList{Policies: []api.DirPolicy{}}
	for {
		page, err := c.ListDirPolicies(ctx, opts)
		if err != nil {
			return nil, err
		}
		out.Policies = append(out.Policies, page.Policies...)
		if page.NextCursor == "" {
			return out, nil
		}
		opts.Cursor = page.NextCursor
	}
}

// ShowDir resolves a directory's public behavior, even without a stored rule.
func (c *Client) ShowDir(ctx context.Context, rawPrefix string) (*api.EffectiveDir, error) {
	prefix, err := api.CleanPrefix(rawPrefix)
	if err != nil {
		return nil, err
	}
	var out api.EffectiveDir
	if err := c.call(ctx, request{method: http.MethodGet, route: api.DirRoute(prefix), idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DirWriteOptions supplies optional optimistic concurrency preconditions.
type DirWriteOptions struct {
	IfRevision int64
	CreateOnly bool
}

func (o DirWriteOptions) headers() http.Header {
	h := http.Header{}
	if o.IfRevision != 0 {
		h.Set("If-Match", api.FormatRevisionETag(o.IfRevision))
	}
	if o.CreateOnly {
		h.Set("If-None-Match", "*")
	}
	return h
}

// PutDir replaces an explicit policy and returns its effective behavior.
func (c *Client) PutDir(ctx context.Context, prefix string, req api.DirPolicyRequest, opts DirWriteOptions) (*api.EffectiveDir, error) {
	return c.writeDir(ctx, http.MethodPut, prefix, req, opts)
}

// PatchDir changes selected fields, creating a rule if necessary.
func (c *Client) PatchDir(ctx context.Context, prefix string, patch api.DirPolicyPatch, opts DirWriteOptions) (*api.EffectiveDir, error) {
	return c.writeDir(ctx, http.MethodPatch, prefix, patch, opts)
}

func (c *Client) writeDir(ctx context.Context, method, raw string, body any, opts DirWriteOptions) (*api.EffectiveDir, error) {
	prefix, err := api.CleanPrefix(raw)
	if err != nil {
		return nil, err
	}
	b, err := jsonBody(body)
	if err != nil {
		return nil, err
	}
	var out api.EffectiveDir
	if err := c.call(ctx, request{method: method, route: api.DirRoute(prefix), header: opts.headers(), jsonBody: b}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteDir removes a rule without removing objects or descendant rules.
func (c *Client) DeleteDir(ctx context.Context, rawPrefix string, opts DirWriteOptions) error {
	prefix, err := api.CleanPrefix(rawPrefix)
	if err != nil {
		return err
	}
	return c.call(ctx, request{method: http.MethodDelete, route: api.DirRoute(prefix), header: opts.headers()}, nil)
}
