package service

import (
	"errors"
	"fmt"

	"go.xchunk.org/vela/api"
)

// Error kinds. The transport maps each to an HTTP status and an API error
// code in exactly one place (internal/server); match with errors.Is.
var (
	ErrInvalidRequest      = errors.New("invalid request")
	ErrInvalidPath         = errors.New("invalid path")
	ErrChecksumMismatch    = errors.New("checksum mismatch")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrForbidden           = errors.New("forbidden")
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("conflict")
	ErrAlreadyExists       = errors.New("already exists")
	ErrPathConflict        = errors.New("path conflict")
	ErrPreconditionFailed  = errors.New("precondition failed")
	ErrTooLarge            = errors.New("payload too large")
	ErrUnsupportedEncoding = errors.New("unsupported encoding")
	// ErrLinkExpired is only produced on the public signed-link route and
	// becomes 410 Gone.
	ErrLinkExpired = errors.New("link expired")
	// ErrUploadInterrupted means the client stopped sending the body.
	ErrUploadInterrupted = errors.New("upload interrupted")
)

// Error is a domain error with a client-safe message and optional details.
type Error struct {
	Kind    error
	Msg     string
	Details map[string]any
}

func (e *Error) Error() string { return e.Msg }

// Unwrap exposes the kind to errors.Is.
func (e *Error) Unwrap() error { return e.Kind }

func newErr(kind error, details map[string]any, format string, args ...any) error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...), Details: details}
}

func invalidf(format string, args ...any) error {
	return newErr(ErrInvalidRequest, nil, format, args...)
}

func notFoundf(format string, args ...any) error {
	return newErr(ErrNotFound, nil, format, args...)
}

// pathError converts a validation failure from package api.
func pathError(err error) error {
	var pe *api.PathError
	if errors.As(err, &pe) {
		return newErr(ErrInvalidPath, map[string]any{"reason": pe.Reason}, "%s", pe.Error())
	}
	return newErr(ErrInvalidPath, nil, "%s", err.Error())
}
