package api

import (
	"fmt"
	"strings"
)

// Visibility controls who can reach an object without an API key.
// See the table in spec section 3.
type Visibility string

const (
	// Public objects are listed in the HTML index and served by direct URL.
	Public Visibility = "public"
	// Unlisted objects are served by direct URL but never listed. This is not
	// access control: anyone who learns the URL can fetch the file.
	Unlisted Visibility = "unlisted"
	// Private objects are reachable only with an API key or a signed link.
	// Their direct URL answers 404 so that their existence is not disclosed.
	Private Visibility = "private"
)

// Visibilities lists all values in display order.
var Visibilities = []Visibility{Public, Unlisted, Private}

// Valid reports whether v is a known visibility.
func (v Visibility) Valid() bool {
	return v == Public || v == Unlisted || v == Private
}

// ParseVisibility parses a visibility name, case-insensitively.
func ParseVisibility(s string) (Visibility, error) {
	v := Visibility(strings.ToLower(strings.TrimSpace(s)))
	if !v.Valid() {
		return "", fmt.Errorf("unknown visibility %q (want public, unlisted or private)", s)
	}
	return v, nil
}

// Disposition selects the Content-Disposition type used when serving content.
type Disposition string

const (
	// Inline lets the browser display the file when it can.
	Inline Disposition = "inline"
	// Attachment makes the browser download the file.
	Attachment Disposition = "attachment"
)

// Valid reports whether d is a known disposition.
func (d Disposition) Valid() bool { return d == Inline || d == Attachment }

// ParseDisposition parses a disposition name, case-insensitively.
func ParseDisposition(s string) (Disposition, error) {
	d := Disposition(strings.ToLower(strings.TrimSpace(s)))
	if !d.Valid() {
		return "", fmt.Errorf("unknown disposition %q (want inline or attachment)", s)
	}
	return d, nil
}

// Scope is the privilege level of an API key (spec section 7.4). Scopes are
// ordered: every scope includes the rights of the ones below it.
type Scope string

const (
	// ScopeRO allows listing, stat and downloads.
	ScopeRO Scope = "ro"
	// ScopeRW adds every mutation except permanent deletion.
	ScopeRW Scope = "rw"
	// ScopeAdmin adds purging (permanent deletion) and emptying the trash.
	ScopeAdmin Scope = "admin"
)

func (s Scope) rank() int {
	switch s {
	case ScopeRO:
		return 1
	case ScopeRW:
		return 2
	case ScopeAdmin:
		return 3
	default:
		return 0
	}
}

// Valid reports whether s is a known scope.
func (s Scope) Valid() bool { return s.rank() > 0 }

// Allows reports whether a key with scope s may perform an operation that
// requires scope need. Unknown scopes allow nothing.
func (s Scope) Allows(need Scope) bool {
	return s.rank() > 0 && s.rank() >= need.rank()
}

// ParseScope parses a scope name, case-insensitively.
func ParseScope(str string) (Scope, error) {
	s := Scope(strings.ToLower(strings.TrimSpace(str)))
	if !s.Valid() {
		return "", fmt.Errorf("unknown scope %q (want ro, rw or admin)", str)
	}
	return s, nil
}
