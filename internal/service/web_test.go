package service_test

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

func sessionHash(t *testing.T, id string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return sum[:]
}

func TestWebSignIn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	k, err := e.svc.CreateKey(e.ctx, "owner", api.ScopeAdmin, "test/")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "Bearer " + k.Full, k.Full[:len(k.Full)-4] + "AAAA", "vela_zzzzzzzzzz_" + k.Full[16:]} {
		_, err := e.svc.SignIn(e.ctx, bad)
		wantErr(t, err, service.ErrUnauthorized)
	}
	in, err := e.svc.SignIn(e.ctx, k.Full)
	if err != nil || len(in.ID) != 43 || !in.ExpiresAt.Equal(t0.Add(service.DefaultWebSessionMax)) {
		t.Fatalf("SignIn = %+v, %v", in, err)
	}
	// The session is read-only whatever the key may do, and it carries the
	// key's prefix.
	p, err := e.svc.WebSession(e.ctx, in.ID)
	if err != nil || p.KeyID != k.Key.ID || p.Prefix != "test/" || p.Scope != api.ScopeRO {
		t.Fatalf("WebSession = %+v, %v", p, err)
	}
	row, err := e.storage.DB.WebSessionByHash(e.ctx, sessionHash(t, in.ID))
	if err != nil || row.KeyID != k.Key.ID {
		t.Fatalf("stored session = %+v, %v", row, err)
	}
	for _, bad := range []string{"", "x", in.ID + "A", in.ID[:42] + "!", base64.RawURLEncoding.EncodeToString(make([]byte, 32))} {
		_, err := e.svc.WebSession(e.ctx, bad)
		wantErr(t, err, service.ErrUnauthorized)
	}
	second, err := e.svc.SignIn(e.ctx, k.Full)
	if err != nil || second.ID == in.ID {
		t.Fatalf("second SignIn = %+v, %v", second, err)
	}

	// Revoking the key ends every session immediately and removes the row.
	if err := e.svc.RevokeKey(e.ctx, k.Key.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.WebSession(e.ctx, in.ID)
	wantErr(t, err, service.ErrUnauthorized)
	if _, err := e.storage.DB.WebSessionByHash(e.ctx, sessionHash(t, in.ID)); err == nil {
		t.Fatal("session of a revoked key kept")
	}
	_, err = e.svc.SignIn(e.ctx, k.Full)
	wantErr(t, err, service.ErrUnauthorized)
}

func TestWebSessionExpiry(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	k, err := e.svc.CreateKey(e.ctx, "owner", api.ScopeRO, "")
	if err != nil {
		t.Fatal(err)
	}
	signIn := func() string {
		t.Helper()
		in, err := e.svc.SignIn(e.ctx, k.Full)
		if err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	valid := func(id string) bool {
		t.Helper()
		_, err := e.svc.WebSession(e.ctx, id)
		if err != nil {
			wantErr(t, err, service.ErrUnauthorized)
		}
		return err == nil
	}
	lastSeen := func(id string) time.Time {
		t.Helper()
		row, err := e.storage.DB.WebSessionByHash(e.ctx, sessionHash(t, id))
		if err != nil {
			t.Fatal(err)
		}
		return row.LastSeenAt
	}

	idle := signIn()
	active := signIn()
	// Activity is recorded at most once per hour.
	e.clock.Advance(30 * time.Minute)
	if !valid(active) || !lastSeen(active).Equal(t0) {
		t.Fatalf("activity recorded within the hour: %v", lastSeen(active))
	}
	e.clock.Advance(30 * time.Minute)
	if !valid(active) || !lastSeen(active).Equal(t0.Add(time.Hour)) {
		t.Fatalf("activity not recorded after an hour: %v", lastSeen(active))
	}
	// The idle timeout slides with activity...
	e.clock.Set(t0.Add(7*api.Day + time.Millisecond))
	if valid(idle) {
		t.Fatal("idle session still valid")
	}
	for day := 7; day < 30; day += 6 {
		e.clock.Set(t0.Add(time.Duration(day) * api.Day))
		if !valid(active) {
			t.Fatalf("active session expired on day %d", day)
		}
	}
	// ...but never beyond the absolute lifetime.
	e.clock.Set(t0.Add(30*api.Day + time.Millisecond))
	if valid(active) {
		t.Fatal("session outlived its absolute lifetime")
	}
}

func TestWebSignOutAndRevokeAll(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	k, err := e.svc.CreateKey(e.ctx, "owner", api.ScopeRO, "")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 3 {
		in, err := e.svc.SignIn(e.ctx, k.Full)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, in.ID)
	}
	for _, id := range []string{ids[0], ids[0], "malformed"} {
		if err := e.svc.SignOut(e.ctx, id); err != nil {
			t.Fatalf("SignOut(%q) = %v", id, err)
		}
	}
	_, err = e.svc.WebSession(e.ctx, ids[0])
	wantErr(t, err, service.ErrUnauthorized)
	if _, err := e.svc.WebSession(e.ctx, ids[1]); err != nil {
		t.Fatalf("other session ended by sign-out: %v", err)
	}
	n, err := e.svc.RevokeWebSessions(e.ctx)
	if err != nil || n != 2 {
		t.Fatalf("RevokeWebSessions = %d, %v", n, err)
	}
	_, err = e.svc.WebSession(e.ctx, ids[2])
	wantErr(t, err, service.ErrUnauthorized)
}

func TestJanitorPurgesWebSessions(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, func(o *service.Options) { o.WebSessionIdle = 2 * time.Hour })
	k, err := e.svc.CreateKey(e.ctx, "owner", api.ScopeRO, "")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := e.svc.SignIn(e.ctx, k.Full)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(90 * time.Minute)
	fresh, err := e.svc.SignIn(e.ctx, k.Full)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour)
	report, err := e.svc.RunJanitor(e.ctx)
	if err != nil || report.Err() != nil {
		t.Fatalf("janitor = %+v, %v", report, err)
	}
	purged := -1
	for _, task := range report.Tasks {
		if task.Name == "web_sessions" {
			purged = task.Count
		}
	}
	if purged != 1 {
		t.Fatalf("web_sessions purged %d", purged)
	}
	if _, err := e.storage.DB.WebSessionByHash(e.ctx, sessionHash(t, stale.ID)); err == nil {
		t.Fatal("idle session survived the janitor")
	}
	if _, err := e.svc.WebSession(e.ctx, fresh.ID); err != nil {
		t.Fatalf("fresh session purged: %v", err)
	}
}
