package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestErrorJSON(t *testing.T) {
	t.Parallel()
	in := ErrorResponse{Error: &Error{
		Code:      CodePathConflict,
		Message:   "a file exists at a parent path: docs/math",
		Details:   map[string]any{"conflicting_path": "docs/math"},
		RequestID: "01jab",
	}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"error":{"code":"path_conflict","message":"a file exists at a parent path: docs/math","details":{"conflicting_path":"docs/math"},"request_id":"01jab"}}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	var out ErrorResponse
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Error.Code != CodePathConflict || out.Error.Status() != http.StatusConflict {
		t.Fatalf("round trip lost data: %+v", out.Error)
	}
}

func TestIsCode(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("upload: %w", &Error{Code: CodeAlreadyExists})
	if !IsCode(err, CodeNotFound, CodeAlreadyExists) {
		t.Error("IsCode did not see wrapped code")
	}
	if IsCode(err, CodeNotFound) || IsCode(fmt.Errorf("plain"), CodeNotFound) {
		t.Error("IsCode false positive")
	}
}

func TestCodeStatus(t *testing.T) {
	t.Parallel()
	tests := map[ErrorCode]int{
		CodeInvalidRequest:      400,
		CodeInvalidPath:         400,
		CodeChecksumMismatch:    400,
		CodeUnauthorized:        401,
		CodeForbidden:           403,
		CodeNotFound:            404,
		CodeMethodNotAllowed:    405,
		CodeAlreadyExists:       409,
		CodePathConflict:        409,
		CodePreconditionFailed:  412,
		CodePayloadTooLarge:     413,
		CodeUnsupportedEncoding: 415,
		CodeRateLimited:         429,
		CodeInternal:            500,
		CodeUnavailable:         503,
		ErrorCode("bogus"):      500,
	}
	for code, want := range tests {
		if got := code.Status(); got != want {
			t.Errorf("%s.Status() = %d; want %d", code, got, want)
		}
	}
}
