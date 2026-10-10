package api

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrorCode is the stable, machine-readable error identifier from spec
// section 10.3. Clients branch on codes, never on messages.
type ErrorCode string

// Error codes. Adding a code is backwards compatible; renaming or removing one
// is a breaking change.
const (
	CodeInvalidRequest      ErrorCode = "invalid_request"
	CodeInvalidPath         ErrorCode = "invalid_path"
	CodeChecksumMismatch    ErrorCode = "checksum_mismatch"
	CodeUnauthorized        ErrorCode = "unauthorized"
	CodeForbidden           ErrorCode = "forbidden"
	CodeNotFound            ErrorCode = "not_found"
	CodeMethodNotAllowed    ErrorCode = "method_not_allowed"
	CodeAlreadyExists       ErrorCode = "already_exists"
	CodePathConflict        ErrorCode = "path_conflict"
	CodePreconditionFailed  ErrorCode = "precondition_failed"
	CodePayloadTooLarge     ErrorCode = "payload_too_large"
	CodeUnsupportedEncoding ErrorCode = "unsupported_encoding"
	CodeRateLimited         ErrorCode = "rate_limited"
	CodeInternal            ErrorCode = "internal"
	CodeUnavailable         ErrorCode = "unavailable"
)

// Status returns the HTTP status that accompanies the code.
func (c ErrorCode) Status() int {
	switch c {
	case CodeInvalidRequest, CodeInvalidPath, CodeChecksumMismatch:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case CodeAlreadyExists, CodePathConflict:
		return http.StatusConflict
	case CodePreconditionFailed:
		return http.StatusPreconditionFailed
	case CodePayloadTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeUnsupportedEncoding:
		return http.StatusUnsupportedMediaType
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Error is the error object of every non-2xx API response. It also
// implements the error interface so that the Go client can return it as is.
type Error struct {
	Code      ErrorCode      `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	RequestID string         `json:"request_id,omitempty"`

	// HTTPStatus is the status the response was received with. It is filled
	// by the client and is not part of the JSON body.
	HTTPStatus int `json:"-"`
}

// ErrorResponse is the JSON envelope: {"error": {...}}.
type ErrorResponse struct {
	Error *Error `json:"error"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Status returns the HTTP status for the error, preferring the one actually
// received over the one implied by the code.
func (e *Error) Status() int {
	if e.HTTPStatus != 0 {
		return e.HTTPStatus
	}
	return e.Code.Status()
}

// IsCode reports whether err is (or wraps) an *Error with one of the codes.
func IsCode(err error, codes ...ErrorCode) bool {
	var ae *Error
	if !errors.As(err, &ae) {
		return false
	}
	for _, c := range codes {
		if ae.Code == c {
			return true
		}
	}
	return false
}
