package client

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"go.xchunk.org/vela/api"
)

// CreateLink issues a signed link. It is not retried automatically.
func (c *Client) CreateLink(ctx context.Context, req api.CreateLinkRequest) (*api.Link, error) {
	req.Path = strings.TrimLeft(req.Path, "/")
	b, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	var l api.Link
	if _, err := c.call(ctx, request{method: http.MethodPost, route: api.RouteLinks, jsonBody: b}, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// LinkListOptions are the parameters of ListLinks.
type LinkListOptions struct {
	// Path restricts the list to links of one object.
	Path           string
	IncludeExpired bool
	IncludeRevoked bool
}

// ListLinks lists signed links, newest first.
func (c *Client) ListLinks(ctx context.Context, opts LinkListOptions) (*api.LinkList, error) {
	q := url.Values{}
	if opts.Path != "" {
		q.Set("path", strings.TrimLeft(opts.Path, "/"))
	}
	if opts.IncludeExpired {
		q.Set("include_expired", "true")
	}
	if opts.IncludeRevoked {
		q.Set("include_revoked", "true")
	}
	var out api.LinkList
	if _, err := c.call(ctx, request{method: http.MethodGet, route: api.RouteLinks, query: q, idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeLink revokes a signed link.
func (c *Client) RevokeLink(ctx context.Context, id string) error {
	_, err := c.call(ctx, request{method: http.MethodDelete, route: api.RouteLinks + "/" + url.PathEscape(id), idempotent: true}, nil)
	return err
}
