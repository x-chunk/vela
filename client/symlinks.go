package client

import (
	"context"
	"net/http"

	"go.xchunk.org/vela/api"
)

// CreateSymlink creates a symlink, or with req.Overwrite retargets an
// existing one in place, and reports whether a new symlink was created. It is
// not retried automatically, like Move: a lost response would turn a retry
// into a spurious "already exists". Use CreateSymlinkTree for recursive
// requests.
func (c *Client) CreateSymlink(ctx context.Context, req api.CreateSymlinkRequest) (*api.Object, bool, error) {
	req.Recursive = false
	b, err := jsonBody(req)
	if err != nil {
		return nil, false, err
	}
	res, err := c.send(ctx, request{method: http.MethodPost, route: api.RouteSymlinks, jsonBody: b, bufferResponse: true})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = res.Body.Close() }()
	var o api.Object
	if err := decodeJSON(res.Body, &o); err != nil {
		return nil, false, err
	}
	return &o, res.StatusCode == http.StatusCreated, nil
}

// CreateSymlinkTree links every object under the prefix req.Source to the
// same relative path under the prefix req.Path in one server transaction.
func (c *Client) CreateSymlinkTree(ctx context.Context, req api.CreateSymlinkRequest) (*api.SymlinkTreeResponse, error) {
	req.Recursive = true
	b, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	var out api.SymlinkTreeResponse
	if err := c.call(ctx, request{method: http.MethodPost, route: api.RouteSymlinks, jsonBody: b}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
