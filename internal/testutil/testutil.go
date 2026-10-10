// Package testutil runs a complete velad in-process for tests of the HTTP
// layer, the Go client and the CLI. It wires the same components as the
// real binary, on a temporary data directory, behind httptest.Server.
//
// It must only be imported from _test.go files.
package testutil

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/clock"
	"go.xchunk.org/vela/internal/index"
	"go.xchunk.org/vela/internal/server"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/wire"
)

// Server is a running in-process velad.
type Server struct {
	// URL is the base URL, also used as the public URL.
	URL      string
	HTTP     *httptest.Server
	Service  *service.Service
	Server   *server.Server
	Storage  *wire.Storage
	Clock    *clock.Fake
	DataDir  string
	AdminKey string
}

type config struct {
	accelPrefix string
	svcOpts     func(*service.Options)
	srvOpts     func(*server.Options)
	start       time.Time
	logOutput   io.Writer
}

// Option customizes NewServer.
type Option func(*config)

// WithAccel enables X-Accel-Redirect delivery with the given prefix.
func WithAccel(prefix string) Option { return func(c *config) { c.accelPrefix = prefix } }

// WithServiceOptions adjusts service options before startup.
func WithServiceOptions(f func(*service.Options)) Option { return func(c *config) { c.svcOpts = f } }

// WithServerOptions adjusts transport options before startup.
func WithServerOptions(f func(*server.Options)) Option { return func(c *config) { c.srvOpts = f } }

// WithStartTime sets the initial fake clock time. The default is the real
// current time, so that TLS-free HTTP clients and file mtimes agree with it.
func WithStartTime(t time.Time) Option { return func(c *config) { c.start = t } }

// WithLogOutput sends server logs to w (default: discarded).
func WithLogOutput(w io.Writer) Option { return func(c *config) { c.logOutput = w } }

// NewServer starts a server and registers its shutdown with t.Cleanup. An
// admin key without prefix is created and stored in AdminKey.
func NewServer(t testing.TB, opts ...Option) *Server {
	t.Helper()
	cfg := config{start: time.Now().UTC().Truncate(time.Second), logOutput: io.Discard}
	for _, o := range opts {
		o(&cfg)
	}
	ctx := context.Background()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(cfg.logOutput, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st, err := wire.OpenStorage(ctx, dir, log)
	if err != nil {
		t.Fatalf("testutil: open storage: %v", err)
	}
	clk := clock.NewFake(cfg.start)

	// The public URL is only known once the listener runs, but the handler
	// needs it; an atomic indirection lets the handler arrive afterwards
	// without a data race.
	var handler atomic.Pointer[http.Handler]
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*handler.Load()).ServeHTTP(w, r)
	}))
	svcOpts := service.Options{
		PublicURL:         ts.URL,
		ServerVersion:     "test",
		MaxUpload:         8 << 20,
		MaxCreate:         64 << 10,
		LinkDefaultTTL:    24 * time.Hour,
		LinkMaxTTL:        30 * api.Day,
		LinkRetention:     7 * api.Day,
		TrashRetention:    14 * api.Day,
		DefaultVisibility: api.Unlisted,
	}
	if cfg.svcOpts != nil {
		cfg.svcOpts(&svcOpts)
	}
	svc, err := wire.NewService(st, clk, log, svcOpts)
	if err != nil {
		t.Fatalf("testutil: service: %v", err)
	}
	if err := svc.EnsureSigningKey(ctx); err != nil {
		t.Fatalf("testutil: signing key: %v", err)
	}
	theme, err := index.Load("")
	if err != nil {
		t.Fatalf("testutil: theme: %v", err)
	}
	srvOpts := server.Options{
		PublicURL:      ts.URL,
		SiteTitle:      "test store",
		AccelPrefix:    cfg.accelPrefix,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")},
		Version:        "test",
		MaxCreateBytes: svcOpts.MaxCreate,
		Clock:          clk,
	}
	if cfg.srvOpts != nil {
		cfg.srvOpts(&srvOpts)
	}
	srv := server.New(svc, theme, log, srvOpts)
	h := srv.Handler()
	handler.Store(&h)

	s := &Server{URL: ts.URL, HTTP: ts, Service: svc, Server: srv, Storage: st, Clock: clk, DataDir: dir}
	t.Cleanup(func() {
		ts.Close()
		_ = st.Close()
	})
	s.AdminKey = s.Key(t, "admin", api.ScopeAdmin, "")
	return s
}

// Key creates an API key and returns its full secret form.
func (s *Server) Key(t testing.TB, name string, scope api.Scope, prefix string) string {
	t.Helper()
	k, err := s.Service.CreateKey(context.Background(), name, scope, prefix)
	if err != nil {
		t.Fatalf("testutil: create key %s: %v", name, err)
	}
	return k.Full
}
