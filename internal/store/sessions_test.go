package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

func TestWebSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	key := domain.APIKey{ID: "abcdefghjk", Name: "owner", SecretHash: []byte{1}, Scope: api.ScopeRO, CreatedAt: t0}
	sess := domain.WebSession{IDHash: []byte("hash-1"), KeyID: key.ID, CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(30 * api.Day)}
	update := func(fn func(tx *Tx) error) {
		t.Helper()
		if err := s.Update(ctx, fn); err != nil {
			t.Fatal(err)
		}
	}
	update(func(tx *Tx) error {
		if err := tx.InsertAPIKey(ctx, key); err != nil {
			return err
		}
		return tx.InsertWebSession(ctx, sess)
	})
	err := s.Update(ctx, func(tx *Tx) error { return tx.InsertWebSession(ctx, sess) })
	if !errors.Is(err, domain.ErrDuplicate) {
		t.Fatalf("duplicate session error = %v", err)
	}
	got, err := s.WebSessionByHash(ctx, sess.IDHash)
	if err != nil || got.KeyID != key.ID || !got.LastSeenAt.Equal(t0) || !got.ExpiresAt.Equal(sess.ExpiresAt) {
		t.Fatalf("WebSessionByHash = %+v, %v", got, err)
	}

	// Activity only moves forward.
	update(func(tx *Tx) error {
		if err := tx.TouchWebSession(ctx, sess.IDHash, t0.Add(2*time.Hour)); err != nil {
			return err
		}
		return tx.TouchWebSession(ctx, sess.IDHash, t0.Add(time.Hour))
	})
	if got, _ := s.WebSessionByHash(ctx, sess.IDHash); !got.LastSeenAt.Equal(t0.Add(2 * time.Hour)) {
		t.Fatalf("last seen = %v", got.LastSeenAt)
	}

	update(func(tx *Tx) error {
		if err := tx.DeleteWebSession(ctx, sess.IDHash); err != nil {
			return err
		}
		return tx.DeleteWebSession(ctx, sess.IDHash) // signing out twice is fine
	})
	if _, err := s.WebSessionByHash(ctx, sess.IDHash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted session lookup error = %v", err)
	}
}

func TestDeleteWebSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	live := domain.APIKey{ID: "abcdefghjk", Name: "live", SecretHash: []byte{1}, Scope: api.ScopeRO, CreatedAt: t0}
	revoked := domain.APIKey{ID: "bcdefghjkm", Name: "revoked", SecretHash: []byte{2}, Scope: api.ScopeRO, CreatedAt: t0}
	now := t0.Add(10 * api.Day)
	idleCutoff := now.Add(-7 * api.Day)
	sessions := map[string]domain.WebSession{
		"fresh":   {KeyID: live.ID, LastSeenAt: now, ExpiresAt: now},
		"idle":    {KeyID: live.ID, LastSeenAt: idleCutoff.Add(-time.Millisecond), ExpiresAt: now.Add(api.Day)},
		"edge":    {KeyID: live.ID, LastSeenAt: idleCutoff, ExpiresAt: now.Add(api.Day)},
		"expired": {KeyID: live.ID, LastSeenAt: now, ExpiresAt: now.Add(-time.Millisecond)},
		"revoked": {KeyID: revoked.ID, LastSeenAt: now, ExpiresAt: now.Add(api.Day)},
		"orphan":  {KeyID: "zzzzzzzzzz", LastSeenAt: now, ExpiresAt: now.Add(api.Day)},
	}
	err := s.Update(ctx, func(tx *Tx) error {
		for _, k := range []domain.APIKey{live, revoked} {
			if err := tx.InsertAPIKey(ctx, k); err != nil {
				return err
			}
		}
		if err := tx.RevokeAPIKey(ctx, revoked.ID, t0); err != nil {
			return err
		}
		for name, sess := range sessions {
			sess.IDHash, sess.CreatedAt = []byte(name), t0
			if err := tx.InsertWebSession(ctx, sess); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	err = s.Update(ctx, func(tx *Tx) error {
		var err error
		n, err = tx.DeleteStaleWebSessions(ctx, now, idleCutoff)
		return err
	})
	if err != nil || n != 4 {
		t.Fatalf("DeleteStaleWebSessions = %d, %v", n, err)
	}
	for name := range sessions {
		_, err := s.WebSessionByHash(ctx, []byte(name))
		if keep := name == "fresh" || name == "edge"; keep != (err == nil) {
			t.Fatalf("session %s: lookup error %v", name, err)
		}
	}
	err = s.Update(ctx, func(tx *Tx) error {
		var err error
		n, err = tx.DeleteWebSessions(ctx)
		return err
	})
	if err != nil || n != 2 {
		t.Fatalf("DeleteWebSessions = %d, %v", n, err)
	}
}
