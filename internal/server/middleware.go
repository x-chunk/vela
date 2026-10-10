package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"go.xchunk.org/vela/api"
)

// reqInfo is per-request state shared between middleware and handlers.
type reqInfo struct {
	id    string
	ip    netip.Addr
	route string
	keyID string
}

type ctxKey struct{}

func info(r *http.Request) *reqInfo {
	if ri, ok := r.Context().Value(ctxKey{}).(*reqInfo); ok {
		return ri
	}
	return &reqInfo{}
}

var requestIDRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// withRequestContext resolves the client IP and the request ID. Both come
// from headers only when the TCP peer is a trusted proxy; otherwise any
// client could forge its address to dodge the auth rate limit, or inject
// misleading request IDs into the logs.
func (s *Server) withRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := remoteAddr(r)
		trusted := s.trusted(peer)
		ri := &reqInfo{ip: peer}
		if trusted {
			ri.ip = forwardedIP(r, peer, s.trusted)
			if id := r.Header.Get(api.HeaderRequestID); requestIDRE.MatchString(id) {
				ri.id = id
			}
		}
		if ri.id == "" {
			ri.id = s.ids.New()
		}
		w.Header().Set(api.HeaderRequestID, ri.id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, ri)))
	})
}

func (s *Server) trusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.opts.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// forwardedIP picks the client address set by trusted proxies. X-Real-IP
// (set by our nginx config) wins; otherwise X-Forwarded-For is walked from
// the right, skipping trusted hops, because only the entries appended by our
// own proxies are trustworthy.
func forwardedIP(r *http.Request, peer netip.Addr, trusted func(netip.Addr) bool) netip.Addr {
	if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return a.Unmap()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !trusted(a) {
			return a
		}
	}
	return peer
}

// statusRecorder captures the status and size for the access log. It keeps
// the optional interfaces http.ResponseController and io.Copy look for.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (rec *statusRecorder) WriteHeader(code int) {
	if rec.status == 0 {
		rec.status = code
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += int64(n)
	return n, err
}

// ReadFrom keeps sendfile(2) available to http.ServeContent in direct mode.
func (rec *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	var n int64
	var err error
	if rf, ok := rec.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(src)
	} else {
		n, err = io.Copy(rec.ResponseWriter, src)
	}
	rec.bytes += n
	return n, err
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (rec *statusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// Flush supports streaming responses.
func (rec *statusRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack is needed by nothing today but keeps the wrapper transparent.
func (rec *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rec.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking not supported")
	}
	return h.Hijack()
}

// withLogging writes one structured line per request. It logs the path but
// never the query string: signed link signatures travel in the query, and
// so would anything else sensitive a client might put there. API keys only
// travel in the Authorization header, which is never logged.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		ri := info(r)
		attrs := []any{
			"method", r.Method,
			"route", ri.route,
			"path", r.URL.EscapedPath(),
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", float64(time.Since(start).Microseconds()) / 1000,
			"request_id", ri.id,
			"ip", ri.ip.String(),
		}
		if ri.keyID != "" {
			attrs = append(attrs, "key_id", ri.keyID)
		}
		level := slogLevelFor(rec.status)
		s.log.Log(r.Context(), level, "request", attrs...)
	})
}

// withRecover turns a panic into a 500 and logs the stack.
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
				panic(v)
			}
			s.log.Error("panic serving request", "request_id", info(r).id, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			if strings.HasPrefix(r.URL.Path, "/api/") {
				s.writeAPIError(w, r, errors.New("panic"))
			} else {
				s.publicError(w, r, http.StatusInternalServerError, "Internal error", "Something went wrong on our side.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// bodyIdleReader extends the connection read deadline before every read, so
// a body may take as long as it needs as long as bytes keep arriving.
type bodyIdleReader struct {
	r       io.ReadCloser
	rc      *http.ResponseController
	timeout time.Duration
}

func (b *bodyIdleReader) Read(p []byte) (int, error) {
	_ = b.rc.SetReadDeadline(time.Now().Add(b.timeout))
	return b.r.Read(p)
}

func (b *bodyIdleReader) Close() error { return b.r.Close() }
