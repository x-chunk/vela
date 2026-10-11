package api

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Path limits from the specification (section 4.1). They are measured in bytes
// of the NFC-normalized UTF-8 string.
const (
	MaxPathBytes    = 1024
	MaxSegmentBytes = 255
)

// reservedFirstSegments are path roots owned by velad itself. Objects cannot
// live under them, otherwise a file could shadow the API, signed links or the
// index assets. Compared case-insensitively.
var reservedFirstSegments = []string{"api", "s", "_vela", "_blobs", "healthz", "robots.txt", "favicon.ico"}

// ErrInvalidPath is the sentinel wrapped by every *PathError.
var ErrInvalidPath = errors.New("invalid path")

// PathError describes why a path was rejected. Reason is a short stable
// machine-readable phrase that the server puts into error details.
type PathError struct {
	Path   string
	Reason string
}

func (e *PathError) Error() string {
	return fmt.Sprintf("invalid path %q: %s", e.Path, e.Reason)
}

// Unwrap lets callers match any path error with errors.Is(err, ErrInvalidPath).
func (e *PathError) Unwrap() error { return ErrInvalidPath }

func pathErr(p, reason string) error { return &PathError{Path: p, Reason: reason} }

// normalize applies the rules shared by object paths and prefixes and reports
// whether the input denoted a directory (trailing slash). The returned string
// keeps the trailing slash for directories and never has a leading one. The
// empty string (or "/") is the root directory.
func normalize(raw string) (string, bool, error) {
	if !utf8.ValidString(raw) {
		return "", false, pathErr(raw, "not valid UTF-8")
	}
	// NFC first, so that the same visible name typed on macOS (NFD) and on
	// Linux (NFC) resolves to the same object.
	p := norm.NFC.String(raw)
	p = strings.TrimLeft(p, "/")
	if p == "" {
		return "", true, nil
	}
	isDir := strings.HasSuffix(p, "/")
	body := strings.TrimSuffix(p, "/")
	if len(body) > MaxPathBytes {
		return "", false, pathErr(raw, fmt.Sprintf("longer than %d bytes", MaxPathBytes))
	}
	segments := strings.Split(body, "/")
	for i, seg := range segments {
		if err := checkSegment(raw, seg); err != nil {
			return "", false, err
		}
		if i == 0 && isReserved(seg) {
			return "", false, pathErr(raw, fmt.Sprintf("%q is a reserved name", seg))
		}
	}
	if isDir {
		return body + "/", true, nil
	}
	return body, false, nil
}

func checkSegment(raw, seg string) error {
	switch {
	case seg == "":
		return pathErr(raw, "empty segment")
	case seg == "." || seg == "..":
		return pathErr(raw, "dot segment")
	case len(seg) > MaxSegmentBytes:
		return pathErr(raw, fmt.Sprintf("segment longer than %d bytes", MaxSegmentBytes))
	}
	for _, r := range seg {
		if r == '\\' {
			return pathErr(raw, "backslash")
		}
		// unicode.IsControl covers C0 (U+0000-U+001F), DEL and C1. C1 controls
		// are not listed in the spec but are never legitimate in a file name and
		// confuse terminals the same way C0 does.
		if unicode.IsControl(r) {
			return pathErr(raw, "control character")
		}
	}
	first, _ := utf8.DecodeRuneInString(seg)
	last, _ := utf8.DecodeLastRuneInString(seg)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return pathErr(raw, "leading or trailing whitespace in segment")
	}
	return nil
}

func isReserved(seg string) bool {
	for _, r := range reservedFirstSegments {
		if strings.EqualFold(seg, r) {
			return true
		}
	}
	return false
}

// CleanObjectPath normalizes a user-supplied object path: strips leading
// slashes, applies NFC and validates it. A trailing slash is rejected because
// objects are never directories.
func CleanObjectPath(raw string) (string, error) {
	p, isDir, err := normalize(raw)
	if err != nil {
		return "", err
	}
	if isDir {
		return "", pathErr(raw, "object path must not end with a slash")
	}
	return p, nil
}

// CleanPrefix normalizes a directory prefix. The result is either "" (root) or
// ends with exactly one slash. A missing trailing slash is added, so "docs" and
// "docs/" denote the same directory.
func CleanPrefix(raw string) (string, error) {
	p, isDir, err := normalize(raw)
	if err != nil {
		return "", err
	}
	if p == "" || isDir {
		return p, nil
	}
	return p + "/", nil
}

// CleanPath normalizes either form and reports whether the input denoted a
// directory (trailing slash or root).
func CleanPath(raw string) (p string, isDir bool, err error) {
	return normalize(raw)
}

// IsWithin reports whether path p (object path or prefix) lies inside prefix.
// The empty prefix contains everything. prefix must come from CleanPrefix.
func IsWithin(p, prefix string) bool {
	return prefix == "" || strings.HasPrefix(p, prefix)
}

// BaseName returns the last segment of an object path or prefix.
func BaseName(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// ParentPrefix returns the directory containing p, as a prefix ("" for root).
func ParentPrefix(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i+1]
	}
	return ""
}

// Ancestors returns the object paths that would conflict with p as files:
// for "a/b/c" that is ["a", "a/b"]. See the file/directory rule in section 4.2.
func Ancestors(p string) []string {
	p = strings.TrimSuffix(p, "/")
	var out []string
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[:i])
		}
	}
	return out
}

// EscapePath percent-encodes every segment separately so that slashes keep
// their meaning while every other byte is safe in a URL path.
func EscapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// PublicURL joins the configured public base URL (without trailing slash) and
// an object path or prefix.
func PublicURL(base, p string) string {
	return strings.TrimSuffix(base, "/") + "/" + EscapePath(p)
}
