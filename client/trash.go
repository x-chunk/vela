package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go.xchunk.org/vela/api"
)

// TrashOptions are the parameters of ListTrash.
type TrashOptions struct {
	// Prefix restricts entries to a directory of original paths.
	Prefix string
	// Path selects entries whose original path is exactly this.
	Path   string
	Limit  int
	Cursor string
}

// ListTrash lists trash entries, newest first.
func (c *Client) ListTrash(ctx context.Context, opts TrashOptions) (*api.TrashList, error) {
	q := url.Values{}
	if opts.Prefix != "" {
		q.Set("prefix", strings.TrimLeft(opts.Prefix, "/"))
	}
	if opts.Path != "" {
		q.Set("path", strings.TrimLeft(opts.Path, "/"))
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	var out api.TrashList
	if err := c.call(ctx, request{method: http.MethodGet, route: api.RouteTrash, query: q, idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListAllTrash follows cursors and returns every matching entry.
func (c *Client) ListAllTrash(ctx context.Context, opts TrashOptions) ([]api.TrashEntry, error) {
	var all []api.TrashEntry
	for {
		page, err := c.ListTrash(ctx, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Entries...)
		if page.NextCursor == "" {
			return all, nil
		}
		opts.Cursor = page.NextCursor
	}
}

// Restore brings a trash entry back. It is not retried automatically.
func (c *Client) Restore(ctx context.Context, id string, req api.RestoreRequest) (*api.Object, error) {
	b, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	var o api.Object
	route := api.RouteTrash + "/" + url.PathEscape(id) + "/restore"
	if err := c.call(ctx, request{method: http.MethodPost, route: route, jsonBody: b}, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// PurgeTrashEntry permanently deletes one trash entry (admin).
func (c *Client) PurgeTrashEntry(ctx context.Context, id string) error {
	return c.call(ctx, request{method: http.MethodDelete, route: api.RouteTrash + "/" + url.PathEscape(id), idempotent: true}, nil)
}

// PurgeTrash permanently deletes trash entries older than olderThan (a
// duration such as "7d"), or all entries if olderThan is empty (admin).
func (c *Client) PurgeTrash(ctx context.Context, olderThan string) (*api.PurgeResponse, error) {
	q := url.Values{}
	if olderThan != "" {
		q.Set("older_than", olderThan)
	}
	var out api.PurgeResponse
	if err := c.call(ctx, request{method: http.MethodDelete, route: api.RouteTrash, query: q, idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
