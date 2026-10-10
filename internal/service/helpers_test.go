package service_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/clock"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/wire"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

type env struct {
	t       *testing.T
	svc     *service.Service
	clock   *clock.Fake
	dataDir string
	admin   service.Principal
	ctx     context.Context
	storage *wire.Storage
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := wire.OpenStorage(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := clock.NewFake(t0)
	svc, err := wire.NewService(st, clk, nil, service.Options{
		PublicURL:         "https://store.example.org",
		ServerVersion:     "test",
		MaxUpload:         1 << 20,
		MaxCreate:         1 << 10,
		LinkDefaultTTL:    24 * time.Hour,
		LinkMaxTTL:        30 * api.Day,
		LinkRetention:     7 * api.Day,
		TrashRetention:    14 * api.Day,
		DefaultVisibility: api.Unlisted,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &env{
		t: t, svc: svc, clock: clk, dataDir: dir, ctx: ctx, storage: st,
		admin: service.Principal{KeyID: "adminkey00", Name: "admin", Scope: api.ScopeAdmin},
	}
}

func (e *env) principal(scope api.Scope, prefix string) service.Principal {
	return service.Principal{KeyID: "key" + string(scope), Name: string(scope), Scope: scope, Prefix: prefix}
}

// put uploads content as admin and fails the test on error.
func (e *env) put(path, content string, vis api.Visibility) api.Object {
	e.t.Helper()
	o, _, err := e.svc.Upload(e.ctx, e.admin, service.UploadInput{
		Path: path, Body: strings.NewReader(content), ContentLength: int64(len(content)), Visibility: &vis,
	})
	if err != nil {
		e.t.Fatalf("put %s: %v", path, err)
	}
	return o
}

func (e *env) read(c service.Content) string {
	e.t.Helper()
	f, err := e.svc.OpenContent(c)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var b bytes.Buffer
	if _, err := b.ReadFrom(f); err != nil {
		e.t.Fatal(err)
	}
	return b.String()
}

func (e *env) countFiles(sub string) int {
	e.t.Helper()
	n := 0
	err := filepath.WalkDir(filepath.Join(e.dataDir, sub), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func wantErr(t *testing.T, err, kind error) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("error = %v; want %v", err, kind)
	}
}

func ptr[T any](v T) *T { return &v }

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

func queryParam(rawURL, key string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return u.Query().Get(key)
}
