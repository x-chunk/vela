package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

// errorCodes maps service error kinds to API codes. This is the only place
// where domain errors become HTTP semantics.
var errorCodes = []struct {
	kind error
	code api.ErrorCode
}{
	{service.ErrInvalidPath, api.CodeInvalidPath},
	{service.ErrInvalidRequest, api.CodeInvalidRequest},
	{service.ErrUploadInterrupted, api.CodeInvalidRequest},
	{service.ErrChecksumMismatch, api.CodeChecksumMismatch},
	{service.ErrUnauthorized, api.CodeUnauthorized},
	{service.ErrForbidden, api.CodeForbidden},
	{service.ErrNotFound, api.CodeNotFound},
	{service.ErrLinkExpired, api.CodeNotFound},
	{service.ErrConflict, api.CodeConflict},
	{service.ErrAlreadyExists, api.CodeAlreadyExists},
	{service.ErrPathConflict, api.CodePathConflict},
	{service.ErrIsSymlink, api.CodeIsSymlink},
	{service.ErrPreconditionFailed, api.CodePreconditionFailed},
	{service.ErrTooLarge, api.CodePayloadTooLarge},
	{service.ErrUnsupportedEncoding, api.CodeUnsupportedEncoding},
	{errRateLimited, api.CodeRateLimited},
	{errMethodNotAllowed, api.CodeMethodNotAllowed},
	{errUnavailable, api.CodeUnavailable},
}

var (
	errRateLimited      = errors.New("rate limited")
	errMethodNotAllowed = errors.New("method not allowed")
	errUnavailable      = errors.New("unavailable")
)

// requestError builds a client error from the transport layer (bad query
// parameters, malformed JSON) with the same shape as service errors.
func requestError(kind error, msg string) error {
	return &service.Error{Kind: kind, Msg: msg}
}

// toAPIError converts any error into the wire representation. Unknown
// errors become "internal" with a generic message; their details go to the
// log only, never to the client.
func toAPIError(err error) (*api.Error, bool) {
	var se *service.Error
	for _, m := range errorCodes {
		if errors.Is(err, m.kind) {
			out := &api.Error{Code: m.code, Message: err.Error()}
			if errors.As(err, &se) {
				out.Message, out.Details = se.Msg, se.Details
			}
			return out, false
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &api.Error{Code: api.CodeUnavailable, Message: "the server is busy; retry later"}, true
	}
	return &api.Error{Code: api.CodeInternal, Message: "internal server error"}, true
}

func (s *Server) writeAPIError(w http.ResponseWriter, r *http.Request, err error) {
	ae, internal := toAPIError(err)
	ri := info(r)
	if internal && !errors.Is(err, context.Canceled) {
		s.log.Error("request failed", "request_id", ri.id, "route", ri.route, "error", err)
	}
	ae.RequestID = ri.id
	if ae.Code == api.CodeRateLimited || ae.Code == api.CodeUnavailable {
		if w.Header().Get("Retry-After") == "" {
			w.Header().Set("Retry-After", "5")
		}
	}
	writeJSON(w, ae.Code.Status(), api.ErrorResponse{Error: ae})
}

// writeJSON writes an API response. API responses are never cached: they
// describe mutable state and are tied to a key.
func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func slogLevelFor(status int) slog.Level {
	if status >= 500 {
		return slog.LevelError
	}
	return slog.LevelInfo
}
