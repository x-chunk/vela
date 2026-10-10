// Package ids generates the identifiers used for objects, blobs, trash
// entries, links and signing keys.
package ids

import (
	"crypto/rand"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Generator produces new unique identifiers.
type Generator interface {
	New() string
}

// ULID generates lower-case ULIDs: 26 Crockford base32 characters, a 48-bit
// millisecond timestamp followed by 80 random bits from crypto/rand. Sorting
// ULIDs sorts by creation time, which gives stable, time-ordered cursors.
//
// The random part matters for security, not just uniqueness: link and object
// IDs appear in URLs, and the 80 random bits make them unguessable.
type ULID struct {
	now func() time.Time
}

// NewULID returns a generator that takes timestamps from now. A nil now uses
// the system clock.
func NewULID(now func() time.Time) *ULID {
	if now == nil {
		now = time.Now
	}
	return &ULID{now: now}
}

// New returns a fresh ULID. It panics only if the operating system's random
// source fails, in which case no secure operation is possible anyway.
func (g *ULID) New() string {
	id := ulid.MustNew(ulid.Timestamp(g.now()), rand.Reader)
	return strings.ToLower(id.String())
}

// Valid reports whether s is a syntactically valid lower-case ULID.
func Valid(s string) bool {
	if len(s) != ulid.EncodedSize || strings.ToLower(s) != s {
		return false
	}
	_, err := ulid.ParseStrict(s)
	return err == nil
}
