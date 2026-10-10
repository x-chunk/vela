// Package server is the HTTP transport of velad: routing, middleware, the
// JSON API under /api/v1, the public side (index, direct URLs, signed links)
// and content delivery. Business rules live in internal/service; this
// package translates between HTTP and service calls and maps service errors
// to API error codes in one place (errors.go).
package server

import (
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"go.xchunk.org/vela/internal/clock"
	"go.xchunk.org/vela/internal/ids"
	"go.xchunk.org/vela/internal/index"
	"go.xchunk.org/vela/internal/service"
)

// Options configure the transport.
type Options struct {
	PublicURL string
	SiteTitle string
	// AccelPrefix enables delivery through nginx X-Accel-Redirect when
	// non-empty (for example "/_blobs/"); empty serves files directly.
	AccelPrefix string
	// TrustedProxies may set X-Real-IP, X-Forwarded-For and X-Request-ID.
	TrustedProxies []netip.Prefix
	// RobotsAllow lets crawlers index the public index.
	RobotsAllow bool
	Version     string
	// BodyIdleTimeout aborts a request whose body stalls for this long. It
	// replaces a global ReadTimeout, which would cap the duration of large
	// uploads instead of detecting dead clients.
	BodyIdleTimeout time.Duration
	// MaxCreateBytes sizes the JSON body limit of POST /api/v1/objects.
	MaxCreateBytes int64
	Clock          clock.Clock
}

// Server serves HTTP.
type Server struct {
	svc      *service.Service
	theme    *index.Theme
	log      *slog.Logger
	opts     Options
	limiter  *authLimiter
	ids      ids.Generator
	api      http.Handler
	draining atomic.Bool
}

// New creates a server.
func New(svc *service.Service, theme *index.Theme, log *slog.Logger, opts Options) *Server {
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.BodyIdleTimeout <= 0 {
		opts.BodyIdleTimeout = time.Minute
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	opts.PublicURL = strings.TrimSuffix(opts.PublicURL, "/")
	s := &Server{
		svc:     svc,
		theme:   theme,
		log:     log,
		opts:    opts,
		limiter: newAuthLimiter(opts.Clock.Now),
		ids:     ids.NewULID(nil),
	}
	s.api = s.apiRoutes()
	return s
}

// SetDraining makes API requests that arrive during shutdown fail fast with
// 503 instead of starting work that the shutdown timeout may cut off.
func (s *Server) SetDraining() { s.draining.Store(true) }

// Handler returns the root handler with all middleware applied.
func (s *Server) Handler() http.Handler {
	return s.withRequestContext(s.withLogging(s.withRecover(http.HandlerFunc(s.dispatch))))
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		s.serveAPI(w, r)
		return
	}
	s.servePublic(w, r)
}

// HTTPServer returns an http.Server with production timeouts. There is no
// ReadTimeout or WriteTimeout: uploads and downloads of large files may
// legitimately take long. Slow-loris style attacks are bounded by
// ReadHeaderTimeout and by the per-read idle deadline on request bodies.
func (s *Server) HTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
}
