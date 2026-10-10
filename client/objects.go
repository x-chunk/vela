package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.xchunk.org/vela/api"
)

// escape strips leading slashes (accepted by the CLI and the server alike)
// and percent-encodes every segment.
func escape(p string) string { return api.EscapePath(strings.TrimLeft(p, "/")) }

// ListOptions are the parameters of List.
type ListOptions struct {
	// Prefix is the directory to list; "" is the root.
	Prefix     string
	Recursive  bool
	Visibility []api.Visibility
	// Limit is the page size (server default 200, maximum 1000).
	Limit  int
	Cursor string
}

func (o ListOptions) query() url.Values {
	q := url.Values{}
	if o.Prefix != "" {
		q.Set("prefix", strings.TrimLeft(o.Prefix, "/"))
	}
	if o.Recursive {
		q.Set("recursive", "true")
	}
	if len(o.Visibility) > 0 {
		vs := make([]string, len(o.Visibility))
		for i, v := range o.Visibility {
			vs[i] = string(v)
		}
		q.Set("visibility", strings.Join(vs, ","))
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Cursor != "" {
		q.Set("cursor", o.Cursor)
	}
	return q
}

// List returns one page of a directory listing.
func (c *Client) List(ctx context.Context, opts ListOptions) (*api.Listing, error) {
	var l api.Listing
	err := c.call(ctx, request{method: http.MethodGet, route: api.RouteObject, query: opts.query(), idempotent: true}, &l)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// ListAll follows cursors and returns every entry under opts.Prefix.
func (c *Client) ListAll(ctx context.Context, opts ListOptions) (*api.Listing, error) {
	all := &api.Listing{Prefix: opts.Prefix, Dirs: []string{}, Objects: []api.Object{}}
	for {
		page, err := c.List(ctx, opts)
		if err != nil {
			return nil, err
		}
		all.Prefix = page.Prefix
		all.Dirs = append(all.Dirs, page.Dirs...)
		all.Objects = append(all.Objects, page.Objects...)
		if page.NextCursor == "" {
			return all, nil
		}
		opts.Cursor = page.NextCursor
	}
}

// Stat returns an object's metadata.
func (c *Client) Stat(ctx context.Context, path string) (*api.Object, error) {
	var o api.Object
	err := c.call(ctx, request{method: http.MethodGet, route: api.RouteObject + "/" + escape(path), idempotent: true}, &o)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// Create stores a small object from inline content. It never overwrites and
// is not retried automatically (a lost response would turn a retry into a
// spurious "already exists").
func (c *Client) Create(ctx context.Context, req api.CreateObjectRequest) (*api.Object, error) {
	b, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	var o api.Object
	err = c.call(ctx, request{method: http.MethodPost, route: api.RouteObject, jsonBody: b}, &o)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// UploadOptions are the optional parts of an upload. Unset fields keep the
// current value when overwriting and take server defaults when creating.
type UploadOptions struct {
	// Size is the body length, or -1 if unknown (chunked transfer).
	Size        int64
	ContentType string
	Visibility  api.Visibility
	Title       *string
	Description *string
	// Tags replaces the tag list when non-nil.
	Tags        []string
	Disposition api.Disposition
	ExpiresAt   *time.Time
	// ClearExpiry removes an existing expiry.
	ClearExpiry bool
	// SHA256 is the expected hex digest; the server rejects a mismatch.
	SHA256 string
	// CreateOnly fails with already_exists instead of overwriting.
	CreateOnly bool
	// IfRevision, when non-zero, only overwrites that revision.
	IfRevision int64
}

// Upload streams body to path. It reports whether a new object was created.
//
// Conditional uploads (CreateOnly or IfRevision) are idempotent and are
// retried when body is an io.Seeker. If a retried CreateOnly upload finds the
// object already there with the same SHA256, the first attempt evidently
// succeeded and its result is returned.
func (c *Client) Upload(ctx context.Context, path string, body io.Reader, opts UploadOptions) (*api.Object, bool, error) {
	h := http.Header{}
	if opts.ContentType != "" {
		h.Set("Content-Type", opts.ContentType)
	} else {
		h.Set("Content-Type", "application/octet-stream")
	}
	if opts.Visibility != "" {
		h.Set(api.HeaderVisibility, string(opts.Visibility))
	}
	if opts.Title != nil {
		h.Set(api.HeaderTitle, api.EncodeHeaderText(*opts.Title))
	}
	if opts.Description != nil {
		h.Set(api.HeaderDescription, api.EncodeHeaderText(*opts.Description))
	}
	if opts.Tags != nil {
		h.Set(api.HeaderTags, api.FormatTagsHeader(opts.Tags))
	}
	if opts.Disposition != "" {
		h.Set(api.HeaderDisposition, string(opts.Disposition))
	}
	switch {
	case opts.ClearExpiry:
		h.Set(api.HeaderExpiresAt, api.ExpiresNone)
	case opts.ExpiresAt != nil:
		h.Set(api.HeaderExpiresAt, opts.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if opts.SHA256 != "" {
		h.Set(api.HeaderSHA256, opts.SHA256)
	}
	if opts.CreateOnly {
		h.Set("If-None-Match", "*")
	}
	if opts.IfRevision != 0 {
		h.Set("If-Match", api.FormatRevisionETag(opts.IfRevision))
	}
	size := opts.Size
	if size == 0 && body == nil {
		body = strings.NewReader("")
	}
	rq := request{
		method:     http.MethodPut,
		route:      api.RouteCont + "/" + escape(path),
		header:     h,
		body:       body,
		size:       size,
		idempotent: opts.CreateOnly || opts.IfRevision != 0,
	}
	res, tries, err := c.sendN(ctx, rq)
	if err != nil {
		if tries > 1 && opts.CreateOnly && opts.SHA256 != "" && api.IsCode(err, api.CodeAlreadyExists) {
			if existing, serr := c.Stat(ctx, path); serr == nil && strings.EqualFold(existing.SHA256, opts.SHA256) {
				return existing, true, nil
			}
		}
		return nil, false, err
	}
	defer func() { _ = res.Body.Close() }()
	var o api.Object
	if err := decodeJSON(res.Body, &o); err != nil {
		return nil, false, err
	}
	return &o, res.StatusCode == http.StatusCreated, nil
}

// DownloadInfo describes downloaded content.
type DownloadInfo struct {
	ContentType string
	// Size is the content length, or -1 if unknown.
	Size int64
	// SHA256 is the digest from the ETag in direct mode; behind nginx the
	// ETag is nginx's own and SHA256 is empty.
	SHA256 string
}

// Download opens an object's content. The caller must close the reader.
func (c *Client) Download(ctx context.Context, path string) (io.ReadCloser, *DownloadInfo, error) {
	res, err := c.send(ctx, request{method: http.MethodGet, route: api.RouteCont + "/" + escape(path), idempotent: true})
	if err != nil {
		return nil, nil, err
	}
	info := &DownloadInfo{ContentType: res.Header.Get("Content-Type"), Size: res.ContentLength}
	if etag := res.Header.Get("ETag"); strings.HasPrefix(etag, `"sha256:`) {
		info.SHA256 = strings.TrimSuffix(strings.TrimPrefix(etag, `"sha256:`), `"`)
	}
	return res.Body, info, nil
}

// Patch updates metadata with a JSON merge patch. When ifRevision is
// non-zero the update only applies to that revision.
func (c *Client) Patch(ctx context.Context, path string, patch api.ObjectPatch, ifRevision int64) (*api.Object, error) {
	b, err := jsonBody(patch)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	if ifRevision != 0 {
		h.Set("If-Match", api.FormatRevisionETag(ifRevision))
	}
	var o api.Object
	err = c.call(ctx, request{method: http.MethodPatch, route: api.RouteObject + "/" + escape(path), header: h, jsonBody: b}, &o)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// Move renames or moves objects. It is not retried automatically.
func (c *Client) Move(ctx context.Context, req api.MoveRequest) (*api.MoveResponse, error) {
	b, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	var out api.MoveResponse
	if err := c.call(ctx, request{method: http.MethodPost, route: api.RouteMove, jsonBody: b}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteOptions are the parameters of Delete.
type DeleteOptions struct {
	// Recursive deletes every object under a directory path.
	Recursive bool
	// Purge deletes permanently instead of moving to the trash (admin).
	Purge      bool
	IfRevision int64
}

// Delete moves objects to the trash or purges them.
func (c *Client) Delete(ctx context.Context, path string, opts DeleteOptions) (*api.DeleteResponse, error) {
	q := url.Values{}
	if opts.Recursive {
		q.Set("recursive", "true")
	}
	if opts.Purge {
		q.Set("purge", "true")
	}
	h := http.Header{}
	if opts.IfRevision != 0 {
		h.Set("If-Match", api.FormatRevisionETag(opts.IfRevision))
	}
	var out api.DeleteResponse
	err := c.call(ctx, request{method: http.MethodDelete, route: api.RouteObject + "/" + escape(path), query: q, header: h, idempotent: true}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func decodeJSON(r io.Reader, out any) error {
	if err := json.NewDecoder(r).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return &NetworkError{Err: errors.New("empty response")}
		}
		return &NetworkError{Err: err}
	}
	return nil
}
