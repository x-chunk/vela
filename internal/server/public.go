package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"path"
	"strings"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/index"
	"go.xchunk.org/vela/internal/service"
)

// indexCSP locks index and error pages down to their own stylesheet and
// images: no scripts, no inline styles, no framing.
const indexCSP = "default-src 'self'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// servePublic dispatches everything outside /api. Routing is done by hand
// rather than with ServeMux so that every miss renders the themed 404 page
// and no path is ever answered with a cleaning redirect.
func (s *Server) servePublic(w http.ResponseWriter, r *http.Request) {
	ri := info(r)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		ri.route = "public"
		w.Header().Set("Allow", "GET, HEAD")
		s.publicError(w, r, http.StatusMethodNotAllowed, "Method not allowed", "Only GET and HEAD are supported here.")
		return
	}
	p := r.URL.Path
	switch {
	case p == api.RouteHealth:
		ri.route = "healthz"
		s.serveHealth(w, r)
	case p == "/robots.txt":
		ri.route = "robots"
		s.serveRobots(w)
	case p == "/favicon.ico":
		ri.route = "favicon"
		s.serveAsset(w, r, "favicon.ico")
	case strings.HasPrefix(p, "/_vela/assets/"):
		ri.route = "asset"
		s.serveAsset(w, r, strings.TrimPrefix(p, "/_vela/assets/"))
	case strings.HasPrefix(p, "/s/"):
		ri.route = "link"
		s.serveLink(w, r)
	case strings.HasSuffix(p, "/"):
		ri.route = "index"
		s.serveIndex(w, r)
	default:
		ri.route = "object"
		s.serveObject(w, r)
	}
}

func (s *Server) site() index.Site {
	return index.Site{Title: s.siteTitle(), URL: s.opts.PublicURL}
}

func (s *Server) siteTitle() string {
	if s.opts.SiteTitle != "" {
		return s.opts.SiteTitle
	}
	return "Vela"
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	prefix := strings.TrimPrefix(r.URL.Path, "/")
	listing, err := s.svc.Index(r.Context(), prefix)
	if err != nil {
		s.publicServiceError(w, r, err, false)
		return
	}
	dirs := make([]index.DirEntry, 0, len(listing.Dirs))
	for _, d := range listing.Dirs {
		dirs = append(dirs, index.DirEntry{Path: d.Path, Count: d.Count, UpdatedAt: d.UpdatedAt})
	}
	page := index.BuildPage(s.site(), s.opts.Version, listing.Prefix, dirs, listing.Files, s.opts.Clock.Now())

	h := w.Header()
	h.Set("Cache-Control", "public, max-age=60")
	h.Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("format") == "json" {
		// Same data as the HTML, so it can expose nothing non-public.
		h.Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(page)
		return
	}
	var buf bytes.Buffer
	if err := s.theme.RenderIndex(&buf, page); err != nil {
		s.log.Error("render index failed", "request_id", info(r).id, "error", err)
		s.publicError(w, r, http.StatusInternalServerError, "Internal error", "The page could not be rendered.")
		return
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", indexCSP)
	_, _ = buf.WriteTo(w)
}

func (s *Server) serveObject(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.ResolvePublic(r.Context(), strings.TrimPrefix(r.URL.Path, "/"))
	if err != nil {
		s.publicServiceError(w, r, err, false)
		return
	}
	if res.RedirectDir != "" {
		// Not an open redirect: the target is always a local, absolute
		// path built from a validated object path, whose segments are
		// non-empty and escaped, so it can never start with "//" or name
		// another host.
		target := "/" + api.EscapePath(res.RedirectDir)
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently) // #nosec G710 -- see above
		return
	}
	s.deliver(w, r, *res.Content, policyFor(res.Content.Object.Visibility))
}

// serveLink serves /s/{linkID}/{filename}. The filename is cosmetic and
// ignored; exp and sig come from the query string.
func (s *Server) serveLink(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/s/")
	linkID, _, _ := strings.Cut(rest, "/")
	q := r.URL.Query()
	c, err := s.svc.ResolveLink(r.Context(), linkID, q.Get("exp"), q.Get("sig"))
	if err != nil {
		s.publicServiceError(w, r, err, true)
		return
	}
	s.deliver(w, r, c, privatePolicy)
}

func (s *Server) serveHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Health(r.Context()); err != nil {
		s.log.Warn("health check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, api.Health{Status: "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, api.Health{Status: "ok"})
}

func (s *Server) serveRobots(w http.ResponseWriter) {
	body := "User-agent: *\nDisallow: /\n"
	if s.opts.RobotsAllow {
		body = "User-agent: *\nAllow: /\nDisallow: /s/\nDisallow: /api/\n"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(body))
}

var assetTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".svg":   "image/svg+xml",
	".ico":   "image/x-icon",
	".png":   "image/png",
	".woff2": "font/woff2",
	".woff":  "font/woff",
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name string) {
	a, ok := s.theme.Asset(name)
	if !ok {
		s.publicError(w, r, http.StatusNotFound, "Not found", "")
		return
	}
	ext := strings.ToLower(path.Ext(a.Name))
	ct := assetTypes[ext]
	if ct == "" {
		ct = mime.TypeByExtension(ext)
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	// URLs produced by the templates carry the content hash; such a URL
	// always names the same bytes and can be cached for good.
	if v := r.URL.Query().Get("v"); v != "" && v == a.Hash {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "public, max-age=3600")
	}
	http.ServeContent(w, r, "", a.ModTime, bytes.NewReader(a.Content))
}

// publicServiceError renders a service error on the public side. Signed link
// errors carry the same no-store, no-referrer, noindex headers as the content
// they stand in for.
func (s *Server) publicServiceError(w http.ResponseWriter, r *http.Request, err error, link bool) {
	if link {
		h := w.Header()
		h.Set("Cache-Control", cachePrivate)
		h.Set("Referrer-Policy", referrerNone)
		h.Set("X-Robots-Tag", noIndex)
	}
	switch {
	case errors.Is(err, service.ErrLinkExpired):
		s.publicError(w, r, http.StatusGone, "Link expired", "This link has expired. Ask its owner for a new one.")
	case errors.Is(err, service.ErrNotFound):
		s.publicError(w, r, http.StatusNotFound, "Not found", "There is nothing at this address.")
	default:
		s.log.Error("public request failed", "request_id", info(r).id, "error", err)
		s.publicError(w, r, http.StatusInternalServerError, "Internal error", "Something went wrong on our side.")
	}
}

func (s *Server) publicError(w http.ResponseWriter, r *http.Request, status int, title, msg string) {
	h := w.Header()
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", indexCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	var buf bytes.Buffer
	err := s.theme.RenderError(&buf, index.ErrorPage{
		Site: s.site(), Status: status, Title: title, Message: msg,
		GeneratedAt: s.opts.Clock.Now(), Version: s.opts.Version,
	})
	if err != nil {
		s.log.Error("render error page failed", "request_id", info(r).id, "error", err)
		buf.Reset()
		buf.WriteString(http.StatusText(status) + "\n")
		h.Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
