package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

// Headers velad hands to nginx alongside X-Accel-Redirect. nginx does not pass
// arbitrary upstream headers through an internal redirect, but the internal
// location can re-emit them from $upstream_http_x_vela_* variables (see
// deploy/nginx). They never reach the client under these names.
const (
	headerAccelRedirect     = "X-Accel-Redirect"
	headerAccelCacheControl = "X-Vela-Cache-Control"
	headerAccelRobots       = "X-Vela-Robots"
	headerAccelReferrer     = "X-Vela-Referrer-Policy"
	headerAccelETag         = "X-Vela-ETag"
	noIndex                 = "noindex, nofollow"
	cacheShared             = "public, max-age=300"
	cachePrivate            = "private, no-store"
	referrerNone            = "no-referrer"
)

// deliveryPolicy holds the caching and privacy headers of a response.
type deliveryPolicy struct {
	cacheControl   string
	robots         string
	referrerPolicy string
	// accelPath is inserted between the accel prefix and the blob path. It
	// routes signed links to their own nginx location, where error logging
	// is off because nginx would log the signature (deploy/nginx/vela.conf).
	accelPath string
}

var (
	// Public content may be overwritten in place, so it is cached briefly
	// rather than marked immutable.
	publicPolicy   = deliveryPolicy{cacheControl: cacheShared}
	unlistedPolicy = deliveryPolicy{cacheControl: cacheShared, robots: noIndex}
	// Signed links and API downloads must not be cached by shared caches,
	// indexed, or leak their URL (which is a credential) via Referer.
	privatePolicy = deliveryPolicy{cacheControl: cachePrivate, robots: noIndex, referrerPolicy: referrerNone}
	linkPolicy    = deliveryPolicy{cacheControl: cachePrivate, robots: noIndex, referrerPolicy: referrerNone, accelPath: "s/"}
)

func policyFor(v api.Visibility) deliveryPolicy {
	switch v {
	case api.Public:
		return publicPolicy
	case api.Unlisted:
		return unlistedPolicy
	default:
		return privatePolicy
	}
}

// deliver serves an object's bytes. The access decision has already been
// made; this function only chooses how the bytes travel. With an accel
// prefix the response is empty and nginx streams the blob itself (sendfile,
// Range, HEAD); otherwise Go streams the selected backend range.
//
// Validators: the ETag is the content's SHA-256, in both modes. nginx's own
// ETag (mtime and size) would collide for same-size content written within
// one second under the same URL, and a client would then keep stale bytes.
// So velad answers If-None-Match itself before redirecting, and the nginx
// locations replace their ETag with X-Vela-ETag and never answer 304 on
// their own. Last-Modified is not sent at all: with one-second resolution it
// has the same collision, and without it clients revalidate (and send
// If-Range) with the ETag.
//
// User-uploaded HTML and SVG are served with their real type so that pages
// work. That is acceptable because the origin holds no cookies or sessions:
// the API authenticates by header only, so a malicious page cannot act on a
// user's behalf.
func (s *Server) deliver(w http.ResponseWriter, r *http.Request, c service.Content, pol deliveryPolicy) {
	o := c.Object
	etag := `"sha256:` + o.SHA256 + `"`
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")

	if s.opts.AccelPrefix != "" {
		if noneMatch(r.Header.Values("If-None-Match"), etag) {
			// Not redirected, so nginx forwards these headers as they are.
			h.Set("ETag", etag)
			setPolicyHeaders(h, pol)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Type", o.ContentType)
		h.Set("Content-Disposition", contentDisposition(o.Disposition, api.BaseName(o.Path)))
		h.Set(headerAccelRedirect, s.opts.AccelPrefix+pol.accelPath+c.BlobPath)
		h.Set(headerAccelETag, etag)
		h.Set(headerAccelCacheControl, pol.cacheControl)
		if pol.robots != "" {
			h.Set(headerAccelRobots, pol.robots)
		}
		if pol.referrerPolicy != "" {
			h.Set(headerAccelReferrer, pol.referrerPolicy)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	h.Set("Content-Type", o.ContentType)
	h.Set("Content-Disposition", contentDisposition(o.Disposition, api.BaseName(o.Path)))
	setPolicyHeaders(h, pol)
	h.Set("ETag", etag)
	if values := r.Header.Values("If-Match"); len(values) > 0 && !strongMatch(values, etag) {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	if noneMatch(r.Header.Values("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Accept-Ranges", "bytes")
	off, length, status := int64(0), o.Size, http.StatusOK
	rangeHeader := r.Header.Get("Range")
	if ir := r.Header.Get("If-Range"); ir != "" && ir != etag {
		rangeHeader = ""
	}
	if r.Method != http.MethodHead && rangeHeader != "" {
		var err error
		off, length, err = parseRange(rangeHeader, o.Size)
		if err != nil {
			h.Set("Content-Range", fmt.Sprintf("bytes */%d", o.Size))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if off != 0 || length != o.Size || !strings.Contains(rangeHeader, ",") {
			status = http.StatusPartialContent
			h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, off+length-1, o.Size))
		}
	}
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	deliveryURL, err := s.svc.ContentURL(r.Context(), c, o.ContentType, h.Get("Content-Disposition"), pol.cacheControl)
	if err != nil {
		s.log.Error("content delivery grant failed", "object_id", o.ID, "error", err)
		h.Del("Content-Length")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if deliveryURL != "" {
		h.Del("Content-Length")
		h.Del("Content-Range")
		h.Set("Cache-Control", cachePrivate)
		h.Set("Referrer-Policy", referrerNone)
		h.Set("Location", deliveryURL)
		w.WriteHeader(http.StatusFound)
		return
	}
	f, err := s.svc.OpenRange(r.Context(), c, off, length)
	if err != nil {
		s.log.Error("open content failed", "request_id", info(r).id, "object_id", o.ID, "error", err)
		h.Del("Content-Length")
		h.Del("Content-Disposition")
		h.Del("Content-Range")
		h.Del("ETag")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(status)
	if _, err := io.CopyBuffer(w, io.LimitReader(f, length), make([]byte, 64<<10)); err != nil && r.Context().Err() == nil {
		s.log.Warn("content stream interrupted", "object_id", o.ID, "error", err)
	}

}

func setPolicyHeaders(h http.Header, pol deliveryPolicy) {
	h.Set("Cache-Control", pol.cacheControl)
	if pol.robots != "" {
		h.Set("X-Robots-Tag", pol.robots)
	}
	if pol.referrerPolicy != "" {
		h.Set("Referrer-Policy", pol.referrerPolicy)
	}
}

// noneMatch evaluates If-None-Match (RFC 9110, section 13.1.2) with weak
// comparison: it reports whether any listed tag, or "*", matches etag.
func noneMatch(values []string, etag string) bool {
	for _, v := range values {
		for _, tag := range strings.Split(v, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
				return true
			}
		}
	}
	return false
}

// contentDisposition builds the header per RFC 6266: an ASCII fallback in
// filename= for old clients and the exact UTF-8 name in filename*=.
func contentDisposition(d api.Disposition, name string) string {
	if d != api.Attachment {
		d = api.Inline
	}
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, d, asciiFallback(name), rfc5987Escape(name))
}

func asciiFallback(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// rfc5987Escape percent-encodes everything except attr-char (RFC 8187).
func rfc5987Escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

// parseRange supports one byte range; multipart ranges deliberately use a full response.
func parseRange(value string, size int64) (int64, int64, error) {
	bad := errors.New("invalid byte range")
	if !strings.HasPrefix(value, "bytes=") {
		return 0, 0, bad
	}
	value = strings.TrimPrefix(value, "bytes=")
	if strings.Contains(value, ",") {
		return 0, size, nil
	}
	a, b, ok := strings.Cut(strings.TrimSpace(value), "-")
	if !ok || size == 0 {
		return 0, 0, bad
	}
	if a == "" {
		n, err := parseByteNumber(b)
		if err != nil || n <= 0 {
			return 0, 0, bad
		}
		n = min(n, size)
		return size - n, n, nil
	}
	start, err := parseByteNumber(a)
	if err != nil || start < 0 || start >= size {
		return 0, 0, bad
	}
	end := size - 1
	if b != "" {
		end, err = parseByteNumber(b)
		if err != nil || end < start {
			return 0, 0, bad
		}
		end = min(end, size-1)
	}
	return start, end - start + 1, nil
}

func strongMatch(values []string, etag string) bool {
	for _, v := range values {
		for _, tag := range strings.Split(v, ",") {
			if tag = strings.TrimSpace(tag); tag == "*" || tag == etag {
				return true
			}
		}
	}
	return false
}

func parseByteNumber(s string) (int64, error) {
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid byte offset")
		}
	}
	return strconv.ParseInt(s, 10, 64)
}
