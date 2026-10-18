package server

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"go.xchunk.org/vela/internal/index"
	"go.xchunk.org/vela/internal/service"
)

// Web sign-in (docs/SPEC-web.md) lets a browser open the public index with
// the view of an API key. The key travels once, in the body of a same-origin
// POST, and is exchanged for a random session ID in an HttpOnly cookie. It is
// never accepted in a URL: ?key= or ?token= is ignored like any other query
// parameter, and the access log omits queries anyway. Sessions are read-only.
const (
	loginPath  = "/_vela/login"
	logoutPath = "/_vela/logout"
	// loginBodyLimit fits the form, whose only field is the key.
	loginBodyLimit = 4 << 10
	// sessionCookie uses the __Host- prefix, so the browser accepts it only
	// with Secure, Path=/ and no Domain: a sibling subdomain cannot plant or
	// shadow it.
	sessionCookie         = "__Host-vela_session"
	insecureSessionCookie = "vela_session"
	// sessionCSP is indexCSP with form-action 'self', which the sign-in and
	// sign-out forms need. Nothing else is relaxed.
	sessionCSP         = "default-src 'self'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	referrerSameOrigin = "same-origin"
	loginFailed        = "Invalid API key."
	loginThrottled     = "Too many failed attempts. Try again later."
)

func (s *Server) cookieName() string {
	if s.opts.InsecureCookie {
		return insecureSessionCookie
	}
	return sessionCookie
}

func (s *Server) cookieValue(r *http.Request) string {
	c, err := r.Cookie(s.cookieName())
	if err != nil {
		return ""
	}
	return c.Value
}

// session returns the principal of the request's web session, or nil for an
// anonymous visitor and whenever web sign-in is disabled. The key is checked
// on every request, so revocation takes effect immediately; the result is
// cached for the rest of the request. A session that cannot be verified
// leaves the request anonymous, which can only show less.
func (s *Server) session(r *http.Request) *service.Principal {
	if !s.opts.WebAuth {
		return nil
	}
	ri := info(r)
	if ri.sessionChecked {
		return ri.session
	}
	ri.sessionChecked = true
	id := s.cookieValue(r)
	if id == "" {
		return nil
	}
	p, err := s.svc.WebSession(r.Context(), id)
	if err != nil {
		if !errors.Is(err, service.ErrUnauthorized) && r.Context().Err() == nil {
			s.log.Error("web session lookup failed", "request_id", ri.id, "error", err)
		}
		return nil
	}
	ri.session, ri.keyID = &p, p.KeyID
	return ri.session
}

// sessionPage is the theme's view of a session; nil for anonymous visitors.
func sessionPage(r *http.Request, p *service.Principal) *index.Session {
	if p == nil {
		return nil
	}
	return &index.Session{Prefix: p.Prefix, Full: p.Prefix == "", SignOutURL: logoutPath + "?next=" + url.QueryEscape(returnPath(r))}
}

// signInURL is the footer link of anonymous pages while sign-in is enabled.
func (s *Server) signInURL(r *http.Request, p *service.Principal) string {
	if !s.opts.WebAuth || p != nil {
		return ""
	}
	return loginPath + "?next=" + url.QueryEscape(returnPath(r))
}

// returnPath is where signing in or out leads back to: the current page, or
// for the sign-in and sign-out routes, the page they were opened from.
func returnPath(r *http.Request) string {
	if p := r.URL.Path; p == loginPath || p == logoutPath {
		return safeNext(r.URL.Query().Get("next"))
	}
	return safeNext(r.URL.EscapedPath())
}

// safeNext keeps a redirect target on this site. Only a root-relative path is
// accepted: "//host" and "/\host" name another host in browsers, which also
// drop tabs and newlines before parsing, so control characters are refused
// too. The query is dropped; anything else becomes "/".
func safeNext(raw string) string {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsRune(raw, '\\') ||
		strings.ContainsFunc(raw, unicode.IsControl) {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	p := u.EscapedPath()
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || p == loginPath || p == logoutPath {
		return "/"
	}
	return p
}

// sameOrigin accepts only form posts from this site's own pages, which stops
// cross-site sign-in and sign-out. Browsers send Sec-Fetch-Site; older ones
// still send Origin with every POST. A sandboxed document sends Origin
// "null", which never matches.
func (s *Server) sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	origin := r.Header.Get("Origin")
	return origin != "" && sameOriginURL(origin, s.opts.PublicURL)
}

func sameOriginURL(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil || ua.Scheme == "" || ua.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Hostname(), ub.Hostname()) &&
		effectivePort(ua) == effectivePort(ub)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// setSessionCookie writes the session cookie; a negative maxAge expires it.
// Secure is dropped only for the explicit plain-HTTP development setting.
func (s *Server) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- see above
		Name:     s.cookieName(),
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   !s.opts.InsecureCookie,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// varyCookie marks a response whose content depends on the session, so that
// shared caches keep anonymous and signed-in variants apart.
func (s *Server) varyCookie(h http.Header) {
	if !s.opts.WebAuth {
		return
	}
	for _, v := range h.Values("Vary") {
		for _, name := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(name), "Cookie") {
				return
			}
		}
	}
	h.Add("Vary", "Cookie")
}

// htmlHeaders sets the policy headers of a themed page. Signed-in pages are
// never stored by any cache and allow forms to post to this site.
func (s *Server) htmlHeaders(h http.Header, signedIn bool) {
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	if signedIn {
		h.Set("Cache-Control", cachePrivate)
		h.Set("Content-Security-Policy", sessionCSP)
	} else {
		h.Set("Content-Security-Policy", indexCSP)
	}
	if s.opts.WebAuth {
		s.varyCookie(h)
		if h.Get("Referrer-Policy") == "" {
			h.Set("Referrer-Policy", referrerSameOrigin)
		}
	}
}

func (s *Server) serveLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.renderLogin(w, r, http.StatusOK, "")
	case http.MethodPost:
		s.signIn(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		s.publicError(w, r, http.StatusMethodNotAllowed, "Method not allowed", "Use the sign-in form.")
	}
}

// signIn exchanges a key from the form for a session. Failures are counted
// per client IP and all look the same; the key is never logged or echoed.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	ri := info(r)
	if !s.sameOrigin(r) {
		s.publicError(w, r, http.StatusForbidden, "Forbidden", "Sign in from this site's own sign-in page.")
		return
	}
	if blocked, wait := s.loginLimiter.blocked(ri.ip); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		s.renderLogin(w, r, http.StatusTooManyRequests, loginThrottled)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, loginBodyLimit)
	// The parse error is not logged: it can quote the body, that is the key.
	// Only the body is read for the key; ParseForm also fills r.Form from
	// the query, which is never consulted.
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, r, http.StatusBadRequest, loginFailed)
		return
	}
	in, err := s.svc.SignIn(r.Context(), strings.TrimSpace(r.PostForm.Get("key")))
	if errors.Is(err, service.ErrUnauthorized) {
		s.loginLimiter.fail(ri.ip)
		s.renderLogin(w, r, http.StatusUnauthorized, loginFailed)
		return
	}
	if err != nil {
		s.log.Error("web sign-in failed", "request_id", ri.id, "error", err)
		s.publicError(w, r, http.StatusInternalServerError, "Internal error", "Something went wrong on our side.")
		return
	}
	ri.keyID = in.Principal.KeyID
	// Replace this browser's previous session instead of leaving it valid.
	if old := s.cookieValue(r); old != "" {
		if err := s.svc.SignOut(r.Context(), old); err != nil {
			s.log.Warn("could not end the previous web session", "request_id", ri.id, "error", err)
		}
	}
	s.setSessionCookie(w, in.ID, int(in.ExpiresAt.Sub(s.opts.Clock.Now()).Seconds()))
	h := w.Header()
	h.Set("Cache-Control", cachePrivate)
	h.Set("Location", safeNext(r.URL.Query().Get("next")))
	w.WriteHeader(http.StatusSeeOther)
}

// serveLogout deletes the session row, so a copied cookie stops working too,
// and expires the cookie.
func (s *Server) serveLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		s.publicError(w, r, http.StatusMethodNotAllowed, "Method not allowed", "Use the sign-out button.")
		return
	}
	if !s.sameOrigin(r) {
		s.publicError(w, r, http.StatusForbidden, "Forbidden", "Sign out from this site's own pages.")
		return
	}
	s.session(r) // records the key in the access log
	if id := s.cookieValue(r); id != "" {
		if err := s.svc.SignOut(r.Context(), id); err != nil {
			s.log.Error("web sign-out failed", "request_id", info(r).id, "error", err)
			s.publicError(w, r, http.StatusInternalServerError, "Internal error", "Something went wrong on our side.")
			return
		}
	}
	s.setSessionCookie(w, "", -1)
	h := w.Header()
	h.Set("Cache-Control", cachePrivate)
	h.Set("Location", safeNext(r.URL.Query().Get("next")))
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, msg string) {
	viewer := s.session(r)
	var buf bytes.Buffer
	err := s.theme.RenderLogin(&buf, index.LoginPage{
		Site: s.site(), Title: "Sign in", Action: loginPath + "?next=" + url.QueryEscape(returnPath(r)), Error: msg, NoIndex: true,
		GeneratedAt: s.opts.Clock.Now(), Version: s.opts.Version, Session: sessionPage(r, viewer),
	})
	if err != nil {
		s.log.Error("render sign-in page failed", "request_id", info(r).id, "error", err)
		s.publicError(w, r, http.StatusInternalServerError, "Internal error", "The page could not be rendered.")
		return
	}
	h := w.Header()
	s.htmlHeaders(h, true)
	h.Set("X-Robots-Tag", noIndex)
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
