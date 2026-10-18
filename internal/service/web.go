package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

// Web sessions let a browser open the public index with the view of an API
// key (docs/SPEC-web.md). A session is a thin wrapper around that key, not a
// new credential: the cookie carries only a random session ID, the database
// stores its SHA-256, and the key is read again on every request, so revoking
// the key ends its sessions immediately. Sessions are always read-only.
const (
	// DefaultWebSessionIdle is the sliding idle timeout of a session.
	DefaultWebSessionIdle = 7 * api.Day
	// DefaultWebSessionMax is the absolute lifetime of a session.
	DefaultWebSessionMax = 30 * api.Day
	// webSessionIDBytes gives session IDs 256 bits of entropy.
	webSessionIDBytes = 32
	// webSessionTouch bounds last_seen_at writes to one per session per hour,
	// so browsing does not turn every page view into a database write.
	webSessionTouch = time.Hour
)

var webSessionEncoding = base64.RawURLEncoding

// WebSignIn is the result of SignIn. ID is the only copy of the session
// secret; it belongs in the cookie and nowhere else.
type WebSignIn struct {
	ID        string
	Principal Principal
	ExpiresAt time.Time
}

// SignIn verifies an API key and opens a web session for it. Every failure is
// the same ErrUnauthorized as in Authenticate.
func (s *Service) SignIn(ctx context.Context, token string) (WebSignIn, error) {
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		return WebSignIn{}, err
	}
	raw := make([]byte, webSessionIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return WebSignIn{}, fmt.Errorf("generate web session id: %w", err)
	}
	now := s.now()
	ws := domain.WebSession{
		IDHash:     webSessionHash(raw),
		KeyID:      p.KeyID,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.opts.WebSessionMax),
	}
	if err := s.store.Update(ctx, func(w Writer) error { return w.InsertWebSession(ctx, ws) }); err != nil {
		return WebSignIn{}, err
	}
	return WebSignIn{ID: webSessionEncoding.EncodeToString(raw), Principal: readOnly(p), ExpiresAt: ws.ExpiresAt}, nil
}

// WebSession resolves a session ID from a cookie to the principal of its key.
// Unknown, malformed, idle, expired and revoked-key sessions are all the same
// ErrUnauthorized; the caller treats the request as anonymous.
func (s *Service) WebSession(ctx context.Context, id string) (Principal, error) {
	unauthorized := newErr(ErrUnauthorized, nil, "invalid or expired web session")
	hash, ok := parseWebSessionID(id)
	if !ok {
		return Principal{}, unauthorized
	}
	ws, err := s.store.WebSessionByHash(ctx, hash)
	if isNotFound(err) {
		return Principal{}, unauthorized
	}
	if err != nil {
		return Principal{}, err
	}
	now := s.now()
	k, err := s.store.APIKeyByID(ctx, ws.KeyID)
	if err != nil && !isNotFound(err) {
		return Principal{}, err
	}
	if err != nil || k.RevokedAt != nil || now.After(ws.ExpiresAt) || now.Sub(ws.LastSeenAt) > s.opts.WebSessionIdle {
		// The row can never authenticate again; removing it now saves the
		// janitor the work. A failure only leaves it for the janitor.
		if err := s.store.Update(ctx, func(w Writer) error { return w.DeleteWebSession(ctx, hash) }); err != nil {
			s.log.Warn("could not delete a stale web session", "key_id", ws.KeyID, "error", err)
		}
		return Principal{}, unauthorized
	}
	if now.Sub(ws.LastSeenAt) >= s.webSessionTouchInterval() {
		if err := s.store.Update(ctx, func(w Writer) error { return w.TouchWebSession(ctx, hash, now) }); err != nil {
			s.log.Warn("could not record web session activity", "key_id", k.ID, "error", err)
		}
	}
	if s.touch.due(k.ID, now) {
		if err := s.store.Update(ctx, func(w Writer) error { return w.TouchAPIKey(ctx, k.ID, now) }); err != nil {
			s.log.Warn("could not record key usage", "key_id", k.ID, "error", err)
		}
	}
	return readOnly(Principal{KeyID: k.ID, Name: k.Name, Scope: k.Scope, Prefix: k.Prefix}), nil
}

// webSessionTouchInterval keeps activity writes hourly, but frequent enough
// for short idle timeouts: a session is only recorded as active every
// interval, so the effective idle timeout can be up to one interval shorter
// than configured.
func (s *Service) webSessionTouchInterval() time.Duration {
	return min(webSessionTouch, s.opts.WebSessionIdle/2)
}

// readOnly limits a session principal to reads whatever its key may do, so a
// session can never authorize a change even if a write path accepted one.
func readOnly(p Principal) Principal {
	p.Scope = api.ScopeRO
	return p
}

// SignOut deletes the session row, so a copied cookie stops working too.
// Signing out an unknown or malformed session succeeds.
func (s *Service) SignOut(ctx context.Context, id string) error {
	hash, ok := parseWebSessionID(id)
	if !ok {
		return nil
	}
	return s.store.Update(ctx, func(w Writer) error { return w.DeleteWebSession(ctx, hash) })
}

// RevokeWebSessions ends every web session and returns how many there were.
func (s *Service) RevokeWebSessions(ctx context.Context) (int, error) {
	var n int
	err := s.store.Update(ctx, func(w Writer) error {
		var err error
		n, err = w.DeleteWebSessions(ctx)
		return err
	})
	return n, err
}

// janitorWebSessions purges sessions that can no longer authenticate.
func (s *Service) janitorWebSessions(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.store.Update(ctx, func(w Writer) error {
		var err error
		n, err = w.DeleteStaleWebSessions(ctx, now, now.Add(-s.opts.WebSessionIdle))
		return err
	})
	return n, err
}

// parseWebSessionID decodes a cookie value and returns the hash it is stored
// under. Only the hash is ever compared, so the lookup cannot leak the ID.
func parseWebSessionID(id string) ([]byte, bool) {
	if len(id) != webSessionEncoding.EncodedLen(webSessionIDBytes) {
		return nil, false
	}
	raw, err := webSessionEncoding.DecodeString(id)
	if err != nil || len(raw) != webSessionIDBytes {
		return nil, false
	}
	return webSessionHash(raw), true
}

func webSessionHash(raw []byte) []byte {
	sum := sha256.Sum256(raw)
	return sum[:]
}
