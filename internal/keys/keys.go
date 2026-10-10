// Package keys defines the API key wire format (spec section 7.1):
//
//	vela_<keyID>_<secret>
//
// keyID is 10 lower-case Crockford base32 characters and identifies the key
// in the database and in logs. secret is 32 random bytes encoded as base64url
// without padding. The "vela_" prefix makes leaked keys easy to find for
// secret scanners and grep.
package keys

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	// Prefix starts every API key.
	Prefix = "vela_"
	// IDLength is the length of the key ID.
	IDLength = 10
	// SecretBytes is the amount of entropy in the secret part.
	SecretBytes = 32
)

// crockford is the Crockford base32 alphabet in lower case (no i, l, o, u),
// which avoids characters that are easy to confuse when read aloud.
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

var secretEncoding = base64.RawURLEncoding

// ErrMalformed is returned for strings that are not API keys.
var ErrMalformed = errors.New("malformed API key")

// Generated is a freshly created key. Full must be shown to the user exactly
// once; only SecretHash is stored.
type Generated struct {
	ID         string
	Full       string
	SecretHash []byte
}

// NewID returns a random key ID.
func NewID() (string, error) {
	var b [IDLength]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate key id: %w", err)
	}
	// 256 is a multiple of 32, so the modulo introduces no bias.
	for i := range b {
		b[i] = crockford[b[i]%32]
	}
	return string(b[:]), nil
}

// Generate creates a new key with the given ID (see NewID). The caller is
// responsible for checking that the ID is unused.
func Generate(id string) (Generated, error) {
	if !validID(id) {
		return Generated{}, fmt.Errorf("invalid key id %q", id)
	}
	secret := make([]byte, SecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return Generated{}, fmt.Errorf("generate key secret: %w", err)
	}
	return Generated{
		ID:         id,
		Full:       Prefix + id + "_" + secretEncoding.EncodeToString(secret),
		SecretHash: hash(secret),
	}, nil
}

// Parse splits a full key into its ID and the SHA-256 hash of its secret.
// The secret itself is not returned, so it cannot leak by accident.
func Parse(full string) (id string, secretHash []byte, err error) {
	rest, ok := strings.CutPrefix(full, Prefix)
	if !ok || len(rest) < IDLength+1 || rest[IDLength] != '_' {
		return "", nil, ErrMalformed
	}
	id = rest[:IDLength]
	if !validID(id) {
		return "", nil, ErrMalformed
	}
	// The secret may itself contain '_' (base64url), so it is everything after
	// the fixed-width ID and its separator.
	secret, decErr := secretEncoding.DecodeString(rest[IDLength+1:])
	if decErr != nil || len(secret) != SecretBytes {
		return "", nil, ErrMalformed
	}
	return id, hash(secret), nil
}

// Verify compares a presented secret hash with the stored one in constant
// time. A plain bytes.Equal would leak, through timing, how many leading bytes
// of the hash an attacker has guessed right.
func Verify(presented, stored []byte) bool {
	return len(stored) == sha256.Size && subtle.ConstantTimeCompare(presented, stored) == 1
}

// Mask hides the secret part of a key for display: vela_<id>_****.
func Mask(full string) string {
	rest, ok := strings.CutPrefix(full, Prefix)
	if !ok || len(rest) < IDLength {
		return "****"
	}
	return Prefix + rest[:IDLength] + "_****"
}

// hash is SHA-256 without a salt or a slow KDF: the secret has 256 bits of
// entropy, so dictionary and brute-force attacks on a leaked hash are moot.
func hash(secret []byte) []byte {
	sum := sha256.Sum256(secret)
	return sum[:]
}

func validID(id string) bool {
	if len(id) != IDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		if !strings.ContainsRune(crockford, rune(id[i])) {
			return false
		}
	}
	return true
}

// ValidID reports whether id has the key ID format.
func ValidID(id string) bool { return validID(id) }
