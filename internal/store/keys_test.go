package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

func TestAPIKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t)
	k := domain.APIKey{ID: "abcdefghjk", Name: "workstation", SecretHash: []byte{1, 2}, Scope: api.ScopeRW, Prefix: "docs/", CreatedAt: t0}
	insertKey := func(k domain.APIKey) error {
		return s.Update(ctx, func(tx *Tx) error { return tx.InsertAPIKey(ctx, k) })
	}
	if err := insertKey(k); err != nil {
		t.Fatal(err)
	}
	same := k
	same.ID = "bcdefghjkm"
	if err := insertKey(same); !errors.Is(err, domain.ErrDuplicate) {
		t.Fatalf("duplicate active name error = %v", err)
	}
	err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.TouchAPIKey(ctx, k.ID, t0.Add(time.Minute)); err != nil {
			return err
		}
		return tx.RevokeAPIKey(ctx, k.ID, t0.Add(time.Hour))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := insertKey(same); err != nil {
		t.Fatalf("name of a revoked key not reusable: %v", err)
	}
	got, err := s.APIKeyByID(ctx, k.ID)
	if err != nil || got.RevokedAt == nil || got.LastUsedAt == nil || got.Scope != api.ScopeRW || got.Prefix != "docs/" {
		t.Fatalf("APIKeyByID = %+v, %v", got, err)
	}
	all, err := s.ListAPIKeys(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAPIKeys = %d, %v", len(all), err)
	}
	err = s.Update(ctx, func(tx *Tx) error { return tx.RevokeAPIKey(ctx, "zzzzzzzzzz", t0) })
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoke missing key error = %v", err)
	}
}
