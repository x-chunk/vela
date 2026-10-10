package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

// apiHandler handles an authenticated API request. Returned errors are
// written as JSON error responses.
type apiHandler func(w http.ResponseWriter, r *http.Request, p service.Principal) error

// JSON body limits for endpoints other than content upload.
const smallBodyLimit = 64 << 10

func (s *Server) apiRoutes() http.Handler {
	mux := http.NewServeMux()
	route := func(pattern string, handlers map[string]apiHandler) {
		var allow []string
		for method, h := range handlers {
			mux.Handle(method+" "+pattern, s.apiEndpoint(pattern, h))
			allow = append(allow, method)
			if method == http.MethodGet {
				allow = append(allow, http.MethodHead)
			}
		}
		sort.Strings(allow)
		// The method-less pattern only matches when no method above does,
		// which is how a 405 with an Allow header is produced.
		mux.Handle(pattern, s.apiEndpoint(pattern, func(w http.ResponseWriter, _ *http.Request, _ service.Principal) error {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			return requestError(errMethodNotAllowed, "method not allowed; allowed: "+strings.Join(allow, ", "))
		}))
	}
	route(api.RouteInfo, map[string]apiHandler{http.MethodGet: s.handleInfo})
	route(api.RouteObject, map[string]apiHandler{http.MethodGet: s.handleList, http.MethodPost: s.handleCreate})
	route(api.RouteObject+"/{path...}", map[string]apiHandler{
		http.MethodGet: s.handleStat, http.MethodPatch: s.handlePatch, http.MethodDelete: s.handleDelete,
	})
	route(api.RouteCont+"/{path...}", map[string]apiHandler{http.MethodGet: s.handleDownload, http.MethodPut: s.handleUpload})
	route(api.RouteMove, map[string]apiHandler{http.MethodPost: s.handleMove})
	route(api.RouteTrash, map[string]apiHandler{http.MethodGet: s.handleTrashList, http.MethodDelete: s.handleTrashPurge})
	route(api.RouteTrash+"/{id}", map[string]apiHandler{http.MethodDelete: s.handleTrashPurgeEntry})
	route(api.RouteTrash+"/{id}/restore", map[string]apiHandler{http.MethodPost: s.handleRestore})
	route(api.RouteLinks, map[string]apiHandler{http.MethodGet: s.handleLinkList, http.MethodPost: s.handleLinkCreate})
	route(api.RouteLinks+"/{id}", map[string]apiHandler{http.MethodDelete: s.handleLinkRevoke})
	mux.Handle("/", s.apiEndpoint("unknown", func(http.ResponseWriter, *http.Request, service.Principal) error {
		return requestError(service.ErrNotFound, "no such API endpoint")
	}))
	return mux
}

// serveAPI authenticates every /api request before routing, so that
// unauthenticated clients cannot even map the endpoints (401 for all).
func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	ri := info(r)
	ri.route = "api"
	if s.draining.Load() {
		w.Header().Set("Connection", "close")
		s.writeAPIError(w, r, requestError(errUnavailable, "the server is shutting down"))
		return
	}
	// Reject non-canonical paths instead of letting ServeMux redirect them:
	// a redirect would silently turn "a//b" into "a/b" for a PUT.
	if !cleanURLPath(r.URL.Path) {
		s.writeAPIError(w, r, &service.Error{Kind: service.ErrInvalidPath, Msg: "the request path contains empty or dot segments"})
		return
	}
	if blocked, wait := s.limiter.blocked(ri.ip); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		s.writeAPIError(w, r, requestError(errRateLimited, "too many failed authentication attempts; retry later"))
		return
	}
	p, err := s.authenticate(r)
	if err != nil {
		if errors.Is(err, service.ErrUnauthorized) {
			s.limiter.fail(ri.ip)
		}
		s.writeAPIError(w, r, err)
		return
	}
	ri.keyID = p.KeyID
	if r.Body != nil {
		r.Body = &bodyIdleReader{r: r.Body, rc: http.NewResponseController(w), timeout: s.opts.BodyIdleTimeout}
	}
	s.api.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
}

// authenticate reads the API key. Only the Authorization header is accepted:
// keys in query strings or cookies end up in access logs, browser history
// and Referer headers.
func (s *Server) authenticate(r *http.Request) (service.Principal, error) {
	h := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || token == "" {
		return service.Principal{}, &service.Error{Kind: service.ErrUnauthorized, Msg: "missing API key: send Authorization: Bearer <key>"}
	}
	return s.svc.Authenticate(r.Context(), strings.TrimSpace(token))
}

func (s *Server) apiEndpoint(pattern string, h apiHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info(r).route = pattern
		if err := h(w, r, principalFrom(r.Context())); err != nil {
			s.writeAPIError(w, r, err)
		}
	})
}

// cleanURLPath reports whether p has no empty, "." or ".." segments (a
// trailing slash is fine).
func cleanURLPath(p string) bool {
	trimmed := strings.TrimSuffix(p, "/")
	return trimmed == "" || path.Clean(trimmed) == trimmed
}

func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request, p service.Principal) error {
	writeJSON(w, http.StatusOK, s.svc.Info(p))
	return nil
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	q := r.URL.Query()
	recursive, err := boolParam(q.Get("recursive"), "recursive")
	if err != nil {
		return err
	}
	var vis []api.Visibility
	for _, raw := range strings.Split(q.Get("visibility"), ",") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		v, err := api.ParseVisibility(raw)
		if err != nil {
			return requestError(service.ErrInvalidRequest, err.Error())
		}
		vis = append(vis, v)
	}
	limit, err := intParam(q.Get("limit"), "limit")
	if err != nil {
		return err
	}
	listing, err := s.svc.List(r.Context(), p, service.ListParams{
		Prefix: q.Get("prefix"), Recursive: recursive, Visibilities: vis, Limit: limit, Cursor: q.Get("cursor"),
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, listing)
	return nil
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	var req api.CreateObjectRequest
	// base64 inflates content by 4/3; allow for that and the JSON envelope.
	limit := s.opts.MaxCreateBytes*4/3 + smallBodyLimit
	if err := decodeJSON(w, r, limit, &req); err != nil {
		return err
	}
	o, err := s.svc.Create(r.Context(), p, req)
	if err != nil {
		return err
	}
	w.Header().Set("Location", api.ObjectRoute(o.Path))
	w.Header().Set("ETag", api.FormatRevisionETag(o.Revision))
	writeJSON(w, http.StatusCreated, o)
	return nil
}

func (s *Server) handleStat(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	o, err := s.svc.Stat(r.Context(), p, r.PathValue("path"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", api.FormatRevisionETag(o.Revision))
	writeJSON(w, http.StatusOK, o)
	return nil
}

func (s *Server) handlePatch(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	ifMatch, err := ifMatchHeader(r)
	if err != nil {
		return err
	}
	var patch api.ObjectPatch
	if err := decodeJSON(w, r, smallBodyLimit, &patch); err != nil {
		return err
	}
	o, err := s.svc.Patch(r.Context(), p, r.PathValue("path"), patch, ifMatch)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", api.FormatRevisionETag(o.Revision))
	writeJSON(w, http.StatusOK, o)
	return nil
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	q := r.URL.Query()
	recursive, err := boolParam(q.Get("recursive"), "recursive")
	if err != nil {
		return err
	}
	purge, err := boolParam(q.Get("purge"), "purge")
	if err != nil {
		return err
	}
	ifMatch, err := ifMatchHeader(r)
	if err != nil {
		return err
	}
	res, err := s.svc.Delete(r.Context(), p, service.DeleteParams{
		Path: r.PathValue("path"), Recursive: recursive, Purge: purge, IfMatch: ifMatch,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	in := service.UploadInput{
		Path:          r.PathValue("path"),
		Body:          r.Body,
		ContentLength: r.ContentLength,
		ContentType:   r.Header.Get("Content-Type"),
	}
	h := r.Header
	if v, ok := headerValue(h, api.HeaderVisibility); ok {
		vis, err := api.ParseVisibility(v)
		if err != nil {
			return requestError(service.ErrInvalidRequest, err.Error())
		}
		in.Visibility = &vis
	}
	for name, dst := range map[string]**string{api.HeaderTitle: &in.Title, api.HeaderDescription: &in.Description} {
		if v, ok := headerValue(h, name); ok {
			text, err := api.DecodeHeaderText(v)
			if err != nil {
				return requestError(service.ErrInvalidRequest, name+" must be percent-encoded UTF-8")
			}
			*dst = &text
		}
	}
	if v, ok := headerValue(h, api.HeaderTags); ok {
		tags := api.ParseTagsHeader(v)
		in.Tags = &tags
	}
	if v, ok := headerValue(h, api.HeaderDisposition); ok {
		d, err := api.ParseDisposition(v)
		if err != nil {
			return requestError(service.ErrInvalidRequest, err.Error())
		}
		in.Disposition = &d
	}
	if v, ok := headerValue(h, api.HeaderExpiresAt); ok {
		if strings.EqualFold(strings.TrimSpace(v), api.ExpiresNone) {
			in.ExpiresAt = api.Null[time.Time]()
		} else {
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
			if err != nil {
				return requestError(service.ErrInvalidRequest, api.HeaderExpiresAt+" must be an RFC 3339 time or \"none\"")
			}
			in.ExpiresAt = api.Some(t)
		}
	}
	if v, ok := headerValue(h, api.HeaderSHA256); ok {
		v = strings.ToLower(strings.TrimSpace(v))
		if b, err := hex.DecodeString(v); err != nil || len(b) != 32 {
			return requestError(service.ErrInvalidRequest, api.HeaderSHA256+" must be 64 hex characters")
		}
		in.SHA256 = v
	}
	if v, ok := headerValue(h, "If-None-Match"); ok {
		if strings.TrimSpace(v) != "*" {
			return requestError(service.ErrInvalidRequest, "If-None-Match only supports \"*\"")
		}
		in.IfNoneMatch = true
	}
	ifMatch, err := ifMatchHeader(r)
	if err != nil {
		return err
	}
	in.IfMatch = ifMatch

	o, created, err := s.svc.Upload(r.Context(), p, in)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", api.FormatRevisionETag(o.Revision))
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", api.ObjectRoute(o.Path))
	}
	writeJSON(w, status, o)
	return nil
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	c, err := s.svc.Download(r.Context(), p, r.PathValue("path"))
	if err != nil {
		return err
	}
	s.deliver(w, r, c, privatePolicy)
	return nil
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	var req api.MoveRequest
	if err := decodeJSON(w, r, smallBodyLimit, &req); err != nil {
		return err
	}
	n, err := s.svc.Move(r.Context(), p, req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, api.MoveResponse{Moved: n})
	return nil
}

func (s *Server) handleTrashList(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	q := r.URL.Query()
	limit, err := intParam(q.Get("limit"), "limit")
	if err != nil {
		return err
	}
	list, err := s.svc.ListTrash(r.Context(), p, service.TrashParams{
		Prefix: q.Get("prefix"), Path: q.Get("path"), Limit: limit, Cursor: q.Get("cursor"),
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

func (s *Server) handleTrashPurge(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	var olderThan *time.Duration
	if v := r.URL.Query().Get("older_than"); v != "" {
		d, err := api.ParseDuration(v)
		if err != nil {
			return requestError(service.ErrInvalidRequest, "older_than: "+err.Error())
		}
		olderThan = &d
	}
	n, err := s.svc.PurgeTrash(r.Context(), p, olderThan)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, api.PurgeResponse{Purged: n})
	return nil
}

func (s *Server) handleTrashPurgeEntry(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	if err := s.svc.PurgeTrashEntry(r.Context(), p, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	var req api.RestoreRequest
	if err := decodeOptionalJSON(w, r, smallBodyLimit, &req); err != nil {
		return err
	}
	o, err := s.svc.Restore(r.Context(), p, r.PathValue("id"), req)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", api.FormatRevisionETag(o.Revision))
	writeJSON(w, http.StatusOK, o)
	return nil
}

func (s *Server) handleLinkCreate(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	var req api.CreateLinkRequest
	if err := decodeJSON(w, r, smallBodyLimit, &req); err != nil {
		return err
	}
	l, err := s.svc.CreateLink(r.Context(), p, req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, l)
	return nil
}

func (s *Server) handleLinkList(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	q := r.URL.Query()
	expired, err := boolParam(q.Get("include_expired"), "include_expired")
	if err != nil {
		return err
	}
	revoked, err := boolParam(q.Get("include_revoked"), "include_revoked")
	if err != nil {
		return err
	}
	list, err := s.svc.ListLinks(r.Context(), p, service.LinkParams{Path: q.Get("path"), IncludeExpired: expired, IncludeRevoked: revoked})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

func (s *Server) handleLinkRevoke(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	if err := s.svc.RevokeLink(r.Context(), p, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Request parsing helpers.

func headerValue(h http.Header, name string) (string, bool) {
	vs, ok := h[http.CanonicalHeaderKey(name)]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}

func ifMatchHeader(r *http.Request) (int64, error) {
	v, ok := headerValue(r.Header, "If-Match")
	if !ok {
		return 0, nil
	}
	rev, err := api.ParseRevisionETag(v)
	if err != nil {
		return 0, requestError(service.ErrInvalidRequest, err.Error())
	}
	return rev, nil
}

func boolParam(v, name string) (bool, error) {
	switch strings.ToLower(v) {
	case "", "0", "false", "no":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	}
	return false, requestError(service.ErrInvalidRequest, name+" must be true or false")
}

func intParam(v, name string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, requestError(service.ErrInvalidRequest, name+" must be an integer")
	}
	return n, nil
}

// decodeJSON strictly decodes a request body: unknown fields and trailing
// data are errors, so a misspelled option is reported instead of ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	if r.Body == nil {
		return requestError(service.ErrInvalidRequest, "request body is required")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &mbe):
			return requestError(service.ErrTooLarge, "request body is too large")
		case errors.Is(err, io.EOF):
			return requestError(service.ErrInvalidRequest, "request body is required")
		default:
			return requestError(service.ErrInvalidRequest, "invalid JSON: "+err.Error())
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return requestError(service.ErrInvalidRequest, "invalid JSON: unexpected data after the object")
	}
	return nil
}

// decodeOptionalJSON is decodeJSON for endpoints whose body may be empty.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	if r.ContentLength == 0 {
		return nil
	}
	err := decodeJSON(w, r, limit, dst)
	var se *service.Error
	if errors.As(err, &se) && se.Msg == "request body is required" {
		return nil
	}
	return err
}
