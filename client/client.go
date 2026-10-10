// Package client is the Go client for the Vela HTTP API v1.
//
// It is public so that other tools (edu-publish, scripts) can talk to Vela
// without shelling out to the CLI:
//
//	c, err := client.New("https://store.xchunk.org", os.Getenv("VELA_KEY"))
//	obj, err := c.Upload(ctx, "edu/math/q1.pdf", f, client.UploadOptions{Size: size, CreateOnly: true})
//
// API errors are returned as *api.Error, so callers can branch on codes with
// api.IsCode. Transport failures are returned as *NetworkError. Idempotent
// requests are retried on network errors, 429 and 5xx responses.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.xchunk.org/vela/api"
)

// DefaultRetries is the number of retries after the first attempt, so an
// idempotent request is tried at most three times.
const DefaultRetries = 2

// Client talks to one Vela server with one API key. It is safe for
// concurrent use.
type Client struct {
	base      *url.URL
	key       string
	hc        *http.Client
	userAgent string
	retries   int
	backoff   func(attempt int) time.Duration
	trace     io.Writer
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the underlying HTTP client (timeouts, proxies, TLS).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// WithRetries sets how many times an idempotent request is retried.
func WithRetries(n int) Option { return func(c *Client) { c.retries = max(n, 0) } }

// WithBackoff replaces the delay before retry number attempt (1-based).
func WithBackoff(f func(attempt int) time.Duration) Option { return func(c *Client) { c.backoff = f } }

// WithTrace writes one line per HTTP exchange to w. The API key is never
// written.
func WithTrace(w io.Writer) Option { return func(c *Client) { c.trace = w } }

// New creates a client for baseURL (for example https://store.xchunk.org).
func New(baseURL, key string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("invalid server URL %q: want http(s)://host", baseURL)
	}
	c := &Client{
		base:      u,
		key:       key,
		hc:        &http.Client{},
		userAgent: "vela-go-client",
		retries:   DefaultRetries,
		backoff:   defaultBackoff,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// BaseURL returns the server URL the client was created with.
func (c *Client) BaseURL() string { return c.base.String() }

func defaultBackoff(attempt int) time.Duration {
	d := 500 * time.Millisecond << (attempt - 1)
	return d + time.Duration(rand.Int64N(int64(250*time.Millisecond))) // #nosec G404 -- jitter, not security
}

// NetworkError is a failure to reach the server or to read its response.
type NetworkError struct {
	Err error
}

func (e *NetworkError) Error() string { return "cannot reach the server: " + e.Err.Error() }

// Unwrap returns the underlying error.
func (e *NetworkError) Unwrap() error { return e.Err }

// ErrIncompatibleAPI is returned by CheckServer when the server speaks a
// different API major version.
var ErrIncompatibleAPI = errors.New("incompatible API version")

// request describes one API call.
type request struct {
	method string
	route  string // escaped path, e.g. api.ObjectRoute(p)
	query  url.Values
	header http.Header
	// jsonBody is re-sent as is on every attempt.
	jsonBody []byte
	// body is an upload body; it is rewound to its starting position before
	// a retry when it is an io.Seeker, otherwise the request is not retried.
	body       io.Reader
	size       int64
	idempotent bool
	// bufferResponse reads a successful response body completely inside the
	// attempt, so that a connection lost while reading it is retried like
	// any other network error. Streaming downloads leave it off.
	bufferResponse bool
}

// maxBufferedResponse bounds a buffered JSON response.
const maxBufferedResponse = 64 << 20

// attemptBody hands the caller's upload body to one attempt. The transport
// closes request bodies when it is done with them, possibly after Do has
// returned; closing the caller's *os.File would make a retry impossible,
// and a late read would race with the rewind. So the transport only sees
// this wrapper: Close detaches it from the underlying reader, and the
// mutex makes Close wait for a read in progress, after which no further
// read reaches the caller's reader.
type attemptBody struct {
	mu     sync.Mutex
	r      io.Reader
	closed bool
}

func (b *attemptBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, errors.New("upload body of a finished attempt")
	}
	return b.r.Read(p)
}

func (b *attemptBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

func (c *Client) send(ctx context.Context, rq request) (*http.Response, error) {
	res, _, err := c.sendN(ctx, rq)
	return res, err
}

// sendN performs a request with retries and also reports how many attempts
// were made.
func (c *Client) sendN(ctx context.Context, rq request) (*http.Response, int, error) {
	attempts := 1
	if rq.idempotent {
		attempts += c.retries
	}
	// A retry resends the body from where the first attempt started, which
	// is not necessarily offset 0 of the reader the caller passed.
	seeker, _ := rq.body.(io.Seeker)
	var start int64
	if seeker != nil {
		pos, err := seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			seeker = nil
		}
		start = pos
	}
	if rq.body != nil && seeker == nil {
		attempts = 1
	}
	var lastErr error
	attempt := 1
	for ; attempt <= attempts; attempt++ {
		if attempt > 1 {
			delay := c.backoff(attempt - 1)
			var ae *api.Error
			if errors.As(lastErr, &ae) && ae.RetryAfter > 0 {
				delay = ae.RetryAfter
			}
			select {
			case <-ctx.Done():
				return nil, attempt - 1, ctx.Err()
			case <-time.After(delay):
			}
			if seeker != nil {
				if _, err := seeker.Seek(start, io.SeekStart); err != nil {
					return nil, attempt - 1, fmt.Errorf("rewind upload body: %w", err)
				}
			}
		}
		var body *attemptBody
		if rq.body != nil {
			body = &attemptBody{r: rq.body}
		}
		res, err := c.attempt(ctx, rq, body)
		if body != nil {
			_ = body.Close() // fence off the transport before any rewind
		}
		if err == nil {
			return res, attempt, nil
		}
		lastErr = err
		if !retryable(ctx, err) {
			break
		}
	}
	return nil, min(attempt, attempts), lastErr
}

func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var ne *NetworkError
	if errors.As(err, &ne) {
		return true
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		s := ae.Status()
		return s == http.StatusTooManyRequests || s >= 500
	}
	return false
}

func (c *Client) attempt(ctx context.Context, rq request, upload *attemptBody) (*http.Response, error) {
	// Routes arrive escaped per segment. Path and RawPath must agree, or
	// url.URL re-escapes and the server sees "%2520" instead of "%20".
	u := *c.base
	u.RawPath = c.base.EscapedPath() + rq.route
	p, err := url.PathUnescape(u.RawPath)
	if err != nil {
		return nil, fmt.Errorf("invalid request path %q: %w", rq.route, err)
	}
	u.Path = p
	if rq.query != nil {
		u.RawQuery = rq.query.Encode()
	}
	var body io.Reader
	switch {
	case rq.jsonBody != nil:
		body = bytes.NewReader(rq.jsonBody)
	case upload != nil:
		body = upload
	}
	req, err := http.NewRequestWithContext(ctx, rq.method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if rq.body != nil {
		req.ContentLength = rq.size
		if rq.size < 0 {
			req.ContentLength = -1
		}
		// Upload bodies must not be read by the transport behind our back
		// on redirects or 307 retries.
		req.GetBody = nil
	}
	for k, vs := range rq.header {
		req.Header[k] = vs
	}
	if rq.jsonBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}

	start := time.Now()
	c.tracef("-> %s %s", rq.method, u.Redacted())
	res, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.tracef("<- error after %s: %v", time.Since(start).Round(time.Millisecond), err)
		return nil, &NetworkError{Err: err}
	}
	c.tracef("<- %s (%s) request_id=%s", res.Status, time.Since(start).Round(time.Millisecond), res.Header.Get(api.HeaderRequestID))
	if res.StatusCode >= 200 && res.StatusCode < 300 || res.StatusCode == http.StatusNotModified {
		if rq.bufferResponse {
			b, err := io.ReadAll(io.LimitReader(res.Body, maxBufferedResponse))
			_ = res.Body.Close()
			if err != nil {
				c.tracef("<- response body lost: %v", err)
				return nil, &NetworkError{Err: fmt.Errorf("read response: %w", err)}
			}
			res.Body = io.NopCloser(bytes.NewReader(b))
		}
		return res, nil
	}
	return nil, decodeError(res)
}

func (c *Client) tracef(format string, args ...any) {
	if c.trace != nil {
		_, _ = fmt.Fprintf(c.trace, format+"\n", args...)
	}
}

// decodeError turns a non-2xx response into *api.Error. Responses that are
// not API JSON (an nginx error page, for example) get a code derived from
// the status.
func decodeError(res *http.Response) error {
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var er api.ErrorResponse
	ae := &api.Error{}
	if json.Unmarshal(b, &er) == nil && er.Error != nil && er.Error.Code != "" {
		ae = er.Error
	} else {
		ae.Code = codeForStatus(res.StatusCode)
		ae.Message = res.Status
		if snippet := strings.TrimSpace(string(b)); snippet != "" && len(snippet) < 200 && !strings.Contains(snippet, "<") {
			ae.Message += ": " + snippet
		}
	}
	ae.HTTPStatus = res.StatusCode
	if ae.RequestID == "" {
		ae.RequestID = res.Header.Get(api.HeaderRequestID)
	}
	if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s > 0 {
		ae.RetryAfter = min(time.Duration(s)*time.Second, 30*time.Second)
	}
	return ae
}

func codeForStatus(status int) api.ErrorCode {
	switch status {
	case 400:
		return api.CodeInvalidRequest
	case 401:
		return api.CodeUnauthorized
	case 403:
		return api.CodeForbidden
	case 404:
		return api.CodeNotFound
	case 405:
		return api.CodeMethodNotAllowed
	case 409:
		return api.CodeAlreadyExists
	case 412:
		return api.CodePreconditionFailed
	case 413:
		return api.CodePayloadTooLarge
	case 429:
		return api.CodeRateLimited
	case 502, 503, 504:
		return api.CodeUnavailable
	default:
		return api.CodeInternal
	}
}

// call performs a request and decodes a JSON response into out (if non-nil).
func (c *Client) call(ctx context.Context, rq request, out any) error {
	rq.bufferResponse = true
	res, err := c.send(ctx, rq)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			return &NetworkError{Err: fmt.Errorf("decode response: %w", err)}
		}
	}
	return nil
}

func jsonBody(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return b, nil
}

// Info returns server and key information.
func (c *Client) Info(ctx context.Context) (*api.Info, error) {
	var info api.Info
	err := c.call(ctx, request{method: http.MethodGet, route: api.RouteInfo, idempotent: true}, &info)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// CheckServer calls Info and fails with ErrIncompatibleAPI if the server's
// API major version differs from the client's.
func (c *Client) CheckServer(ctx context.Context) (*api.Info, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}
	if info.APIVersion != api.APIVersion {
		return info, fmt.Errorf("%w: server speaks API v%d, this client v%d", ErrIncompatibleAPI, info.APIVersion, api.APIVersion)
	}
	return info, nil
}
