// Package sign implements the HMAC signatures of signed links (spec section 8).
//
// A link URL carries its expiry in clear text (exp) next to a signature over
// the link ID and that expiry, so a forged or extended expiry is detected
// without trusting the client. Which key signed a link is recorded in the
// links table, so the URL itself does not carry a key ID. Key storage and
// rotation live in the service layer; this package is pure crypto.
package sign

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
)

const (
	// KeySize is the size of a signing key: the HMAC-SHA256 block-size-safe
	// recommendation of at least the hash length.
	KeySize = 32
	// sigBytes truncates the MAC to 128 bits, which is plenty against online
	// forgery (every guess costs an HTTP request) and keeps URLs short.
	sigBytes = 16
	// domain separates link signatures from any other use of the same key.
	domain = "vela-link-v1"
)

var sigEncoding = base64.RawURLEncoding

// NewKey returns a fresh random signing key.
func NewKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	return k, nil
}

func mac(key []byte, linkID string, exp int64) []byte {
	m := hmac.New(sha256.New, key)
	// Newline separators make the encoding unambiguous: link IDs are ULIDs and
	// exp is decimal, neither can contain '\n'.
	m.Write([]byte(domain + "\n" + linkID + "\n" + strconv.FormatInt(exp, 10)))
	return m.Sum(nil)[:sigBytes]
}

// Sign returns the URL-safe signature for a link ID and expiry (Unix seconds).
func Sign(key []byte, linkID string, exp int64) string {
	return sigEncoding.EncodeToString(mac(key, linkID, exp))
}

// Verify checks a signature in constant time (hmac.Equal), so response timing
// does not reveal how many leading bytes of a forged signature are right.
func Verify(key []byte, linkID string, exp int64, sig string) bool {
	got, err := sigEncoding.DecodeString(sig)
	if err != nil || len(got) != sigBytes || len(key) == 0 {
		return false
	}
	return hmac.Equal(got, mac(key, linkID, exp))
}
