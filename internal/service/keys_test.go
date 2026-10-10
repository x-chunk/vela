package service_test

import (
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

func TestKeysAndAuthentication(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	k, err := e.svc.CreateKey(e.ctx, "edu-publish", api.ScopeRW, "/edu")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.Full, "vela_"+k.Key.ID+"_") || k.Key.Prefix != "edu/" {
		t.Fatalf("created key = %+v", k)
	}
	_, err = e.svc.CreateKey(e.ctx, "edu-publish", api.ScopeRO, "")
	wantErr(t, err, service.ErrAlreadyExists)
	_, err = e.svc.CreateKey(e.ctx, "bad name", api.ScopeRO, "")
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.CreateKey(e.ctx, "x", api.Scope("root"), "")
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.CreateKey(e.ctx, "x", api.ScopeRO, "api/")
	wantErr(t, err, service.ErrInvalidPath)

	p, err := e.svc.Authenticate(e.ctx, k.Full)
	if err != nil || p.KeyID != k.Key.ID || p.Scope != api.ScopeRW || p.Prefix != "edu/" || p.Name != "edu-publish" {
		t.Fatalf("Authenticate = %+v, %v", p, err)
	}
	keys, _ := e.svc.ListKeys(e.ctx)
	if len(keys) != 1 || keys[0].LastUsedAt == nil {
		t.Fatalf("last_used_at not recorded: %+v", keys)
	}

	wrongSecret := k.Full[:len(k.Full)-4] + "AAAA"
	for _, bad := range []string{"", "Bearer x", wrongSecret, "vela_zzzzzzzzzz_" + k.Full[16:]} {
		_, err := e.svc.Authenticate(e.ctx, bad)
		wantErr(t, err, service.ErrUnauthorized)
	}

	if err := e.svc.RevokeKey(e.ctx, k.Key.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Authenticate(e.ctx, k.Full)
	wantErr(t, err, service.ErrUnauthorized)
	wantErr(t, e.svc.RevokeKey(e.ctx, "zzzzzzzzzz"), service.ErrNotFound)
	// The name is free again after revocation.
	if _, err := e.svc.CreateKey(e.ctx, "edu-publish", api.ScopeRW, "edu/"); err != nil {
		t.Fatal(err)
	}
}

func TestInfo(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	info := e.svc.Info(e.principal(api.ScopeRO, "edu/"))
	if info.APIVersion != 1 || info.PublicURL != "https://store.example.org" || info.Key.Prefix != "edu/" ||
		info.Limits.LinkDefaultTTL != "24h" || info.Limits.LinkMaxTTL != "720h" || info.Limits.MaxCreateBytes != 1024 {
		t.Fatalf("info = %+v", info)
	}
}
