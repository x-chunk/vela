package server

import (
	"fmt"
	"net/http"
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
}

var (
	// Public content may be overwritten in place, so it is cached briefly
	// rather than marked immutable.
	publicPolicy   = deliveryPolicy{cacheControl: cacheShared}
	unlistedPolicy = deliveryPolicy{cacheControl: cacheShared, robots: noIndex}
	// Signed links and API downloads must not be cached by shared caches,
	// indexed, or leak their URL (which is a credential) via Referer.
	privatePolicy = deliveryPolicy{cacheControl: cachePrivate, robots: noIndex, referrerPolicy: referrerNone}
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
// Range, HEAD, conditional requests); otherwise http.ServeContent does the
// same from Go.
//
// User-uploaded HTML and SVG are served with their real type so that pages
// work. That is acceptable because the origin holds no cookies or sessions:
// the API authenticates by header only, so a malicious page cannot act on a
// user's behalf.
func (s *Server) deliver(w http.ResponseWriter, r *http.Request, c service.Content, pol deliveryPolicy) {
	o := c.Object
	h := w.Header()
	h.Set("Content-Type", o.ContentType)
	h.Set("Content-Disposition", contentDisposition(o.Disposition, api.BaseName(o.Path)))
	h.Set("X-Content-Type-Options", "nosniff")

	if s.opts.AccelPrefix != "" {
		h.Set(headerAccelRedirect, s.opts.AccelPrefix+c.BlobPath)
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

	h.Set("Cache-Control", pol.cacheControl)
	if pol.robots != "" {
		h.Set("X-Robots-Tag", pol.robots)
	}
	if pol.referrerPolicy != "" {
		h.Set("Referrer-Policy", pol.referrerPolicy)
	}
	h.Set("ETag", `"sha256:`+o.SHA256+`"`)
	f, err := s.svc.OpenContent(c)
	if err != nil {
		s.log.Error("open content failed", "request_id", info(r).id, "object_id", o.ID, "error", err)
		h.Del("Content-Disposition")
		h.Del("ETag")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	http.ServeContent(w, r, "", o.UpdatedAt, f)
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
