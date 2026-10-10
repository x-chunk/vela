package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/cli/config"
	"go.xchunk.org/vela/internal/cli/output"
)

// Exit codes (spec section 12.5). Scripts depend on them; changing one is a
// breaking change.
const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitNotFound = 3
	exitConflict = 4
	exitAuth     = 5
	exitNetwork  = 6
	exitPartial  = 7
)

// cliError is an error raised by the CLI itself, with an exit code, a
// machine-readable slug for JSON output and an optional hint.
type cliError struct {
	code    int
	slug    string
	msg     string
	hint    string
	details map[string]any
	err     error
}

func (e *cliError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return e.err.Error()
}

func (e *cliError) Unwrap() error { return e.err }

func usageError(err error) error {
	return &cliError{code: exitUsage, slug: "usage", err: err}
}

func usagef(format string, args ...any) error {
	return usageError(fmt.Errorf(format, args...))
}

// hinted attaches a suggestion to an error without changing its code.
type hinted struct {
	err  error
	hint string
}

func (h *hinted) Error() string { return h.err.Error() }
func (h *hinted) Unwrap() error { return h.err }

func withHint(err error, hint string) error {
	if err == nil {
		return nil
	}
	return &hinted{err: err, hint: hint}
}

// hintFor adds a hint when an error has a code-specific remedy.
func hintFor(err error, code api.ErrorCode, hint string) error {
	if api.IsCode(err, code) {
		return withHint(err, hint)
	}
	return err
}

// exitCode maps an error to the process exit status.
func exitCode(err error) int {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.code
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		switch ae.Code {
		case api.CodeNotFound:
			return exitNotFound
		case api.CodeAlreadyExists, api.CodePathConflict, api.CodePreconditionFailed:
			return exitConflict
		case api.CodeUnauthorized, api.CodeForbidden:
			return exitAuth
		case api.CodeUnavailable:
			return exitNetwork
		}
		return exitError
	}
	var ne *client.NetworkError
	if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return exitNetwork
	}
	var pe *config.PermissionError
	if errors.As(err, &pe) {
		return exitError
	}
	if errors.Is(err, client.ErrIncompatibleAPI) {
		return exitError
	}
	// Cobra reports these without a typed error.
	msg := err.Error()
	for _, s := range []string{"unknown command", "unknown flag", "unknown shorthand flag", "required flag", "flags in the group"} {
		if strings.Contains(msg, s) {
			return exitUsage
		}
	}
	return exitError
}

// defaultHint suggests what to do about common failures.
func defaultHint(err error) string {
	var h *hinted
	if errors.As(err, &h) {
		return h.hint
	}
	var ce *cliError
	if errors.As(err, &ce) && ce.hint != "" {
		return ce.hint
	}
	switch {
	case api.IsCode(err, api.CodeUnauthorized):
		return "the key is invalid or revoked; run \"vela login\""
	case api.IsCode(err, api.CodeRateLimited):
		return "too many failed logins from this address; wait a minute"
	case exitCode(err) == exitNetwork:
		return "check the server URL (vela whoami) and your connection"
	case exitCode(err) == exitUsage:
		return "see --help"
	}
	return ""
}

// reportError prints err and returns the exit code. In JSON mode the error
// goes to stdout as {"error": {...}} so that scripts read one document.
func (a *App) reportError(err error) int {
	code := exitCode(err)
	hint := defaultHint(err)
	if a.jsonOut {
		out := &api.Error{Message: err.Error()}
		var ae *api.Error
		var ce *cliError
		switch {
		case errors.As(err, &ae):
			cp := *ae
			out = &cp
		case errors.As(err, &ce):
			out.Code = api.ErrorCode(ce.slug)
			out.Details = ce.details
		default:
			out.Code = api.ErrorCode(slugFor(code))
		}
		if hint != "" {
			if out.Details == nil {
				out.Details = map[string]any{}
			}
			out.Details["hint"] = hint
		}
		_ = a.printer.JSONValue(api.ErrorResponse{Error: out})
		return code
	}
	msg := err.Error()
	var ae *api.Error
	if errors.As(err, &ae) && ae.Message != "" {
		msg = ae.Message
	}
	// Messages can carry data (paths, server text): Errorf cleans them.
	a.printer.Errorf("vela:", "%s", msg)
	if hint != "" {
		_, _ = fmt.Fprintf(a.Err, "  hint: %s\n", output.Clean(hint))
	}
	if ae != nil && ae.Code == api.CodeInternal && ae.RequestID != "" {
		_, _ = fmt.Fprintf(a.Err, "  request id: %s\n", output.Clean(ae.RequestID))
	}
	return code
}

func slugFor(code int) string {
	switch code {
	case exitUsage:
		return "usage"
	case exitNetwork:
		return "network"
	case exitPartial:
		return "partial_failure"
	default:
		return "error"
	}
}

// itemError is one failure in a batch operation.
type itemError struct {
	Path string     `json:"path"`
	Err  *api.Error `json:"error"`
}

// partialError reports a batch where some items failed.
func partialError(op string, total int, failures []itemError) error {
	return &cliError{
		code:    exitPartial,
		slug:    "partial_failure",
		msg:     fmt.Sprintf("%s: %d of %d failed", op, len(failures), total),
		details: map[string]any{"failed": failures},
	}
}

// asAPIError renders any error in the API error shape for batch reports.
func asAPIError(err error) *api.Error {
	var ae *api.Error
	if errors.As(err, &ae) {
		cp := *ae
		if h := defaultHint(err); h != "" {
			cp.Details = map[string]any{"hint": h}
		}
		return &cp
	}
	return &api.Error{Code: api.ErrorCode(slugFor(exitCode(err))), Message: err.Error()}
}

// batchResult finishes a batch: a single item fails with its own error and
// exit code, several items with a partial failure (exit 7).
func batchResult(op string, total int, errs []error, paths []string) error {
	var failures []itemError
	var first error
	for i, err := range errs {
		if err == nil {
			continue
		}
		if first == nil {
			first = err
		}
		failures = append(failures, itemError{Path: paths[i], Err: asAPIError(err)})
	}
	switch {
	case len(failures) == 0:
		return nil
	case total == 1:
		return first
	default:
		return partialError(op, total, failures)
	}
}
