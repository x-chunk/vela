package velad

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/keys"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/wire"
)

type run struct {
	stdout, stderr string
	err            error
}

func newEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"VELA_DATA_DIR":   t.TempDir(),
		"VELA_PUBLIC_URL": "http://store.test",
		"VELA_LISTEN":     "127.0.0.1:0",
		"VELA_LOG_FORMAT": "text",
	}
}

func execute(t *testing.T, ctx context.Context, vars map[string]string, onListen func(net.Addr), args ...string) run {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := NewCommand(Env{
		Stdout:    &out,
		Stderr:    &errOut,
		LookupEnv: func(k string) (string, bool) { v, ok := vars[k]; return v, ok },
		OnListen:  onListen,
	})
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return run{stdout: out.String(), stderr: errOut.String(), err: err}
}

func TestKeyCommands(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vars := newEnv(t)
	r := execute(t, ctx, vars, nil, "key", "create", "--name", "ci", "--scope", "rw", "--prefix", "ci")
	if r.err != nil {
		t.Fatal(r.err, r.stderr)
	}
	full := strings.TrimSpace(r.stdout)
	id, _, err := keys.Parse(full)
	if err != nil {
		t.Fatalf("stdout is not just the key: %q", r.stdout)
	}
	if !strings.Contains(r.stderr, "prefix ci/") || strings.Contains(r.stderr, full) {
		t.Fatalf("stderr = %q", r.stderr)
	}
	r = execute(t, ctx, vars, nil, "key", "list")
	if r.err != nil || !strings.Contains(r.stdout, id) || !strings.Contains(r.stdout, "ci") || strings.Contains(r.stdout, full[16:]) {
		t.Fatalf("list = %q, %v", r.stdout, r.err)
	}
	if r := execute(t, ctx, vars, nil, "key", "create", "--name", "x", "--scope", "root"); r.err == nil {
		t.Fatal("invalid scope accepted")
	}
	if r := execute(t, ctx, vars, nil, "key", "create", "--scope", "ro"); r.err == nil {
		t.Fatal("missing --name accepted")
	}
	if r := execute(t, ctx, vars, nil, "key", "revoke", id); r.err != nil || !strings.Contains(r.stdout, "Revoked "+id) {
		t.Fatalf("revoke = %+v", r)
	}
	if r := execute(t, ctx, vars, nil, "key", "revoke", "zzzzzzzzzz"); r.err == nil {
		t.Fatal("revoking an unknown key succeeded")
	}
}

func TestMaintenanceCommands(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vars := newEnv(t)
	if r := execute(t, ctx, vars, nil, "migrate"); r.err != nil || !strings.Contains(r.stdout, "from version 0 to ") {
		t.Fatalf("migrate = %+v", r)
	}
	if r := execute(t, ctx, vars, nil, "migrate"); r.err != nil || !strings.Contains(r.stdout, "up to date") {
		t.Fatalf("second migrate = %+v", r)
	}
	r := execute(t, ctx, vars, nil, "signing", "rotate")
	if r.err != nil || !strings.Contains(r.stdout, "New signing key") {
		t.Fatalf("rotate = %+v", r)
	}
	r = execute(t, ctx, vars, nil, "signing", "rotate", "--revoke-all")
	if r.err != nil || !strings.Contains(r.stdout, "Destroyed 1 old key(s)") {
		t.Fatalf("rotate --revoke-all = %+v", r)
	}
	r = execute(t, ctx, vars, nil, "janitor", "run")
	if r.err != nil || !strings.Contains(r.stdout, "orphan_blobs") {
		t.Fatalf("janitor = %+v", r)
	}
	if r := execute(t, ctx, vars, nil, "version"); !strings.HasPrefix(r.stdout, "velad ") {
		t.Fatalf("version = %q", r.stdout)
	}
	if r := execute(t, ctx, map[string]string{"VELA_DATA_DIR": t.TempDir()}, nil, "serve"); r.err == nil ||
		!strings.Contains(r.err.Error(), "VELA_PUBLIC_URL") {
		t.Fatalf("serve without public URL: %v", r.err)
	}
	web := map[string]string{"VELA_DATA_DIR": t.TempDir(), "VELA_PUBLIC_URL": "http://store.test", "VELA_WEB_AUTH": "on"}
	if r := execute(t, ctx, web, nil, "serve"); r.err == nil || !strings.Contains(r.err.Error(), "VELA_WEB_AUTH") {
		t.Fatalf("serve web sign-in over plain HTTP: %v", r.err)
	}
}

func TestSessionRevokeAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vars := newEnv(t)
	r := execute(t, ctx, vars, nil, "key", "create", "--name", "owner", "--scope", "ro")
	if r.err != nil {
		t.Fatal(r.err)
	}
	st, err := wire.OpenStorage(ctx, vars["VELA_DATA_DIR"], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	svc, err := wire.NewService(st, nil, nil, service.Options{
		MaxUpload: 1, MaxCreate: 1, LinkDefaultTTL: time.Hour, LinkMaxTTL: time.Hour, DefaultVisibility: api.Private,
	})
	if err != nil {
		t.Fatal(err)
	}
	in, err := svc.SignIn(ctx, strings.TrimSpace(r.stdout))
	if err != nil {
		t.Fatal(err)
	}
	if r := execute(t, ctx, vars, nil, "session", "revoke-all"); r.err != nil || r.stdout != "Revoked 1 web session(s).\n" {
		t.Fatalf("revoke-all = %+v", r)
	}
	if _, err := svc.WebSession(ctx, in.ID); err == nil {
		t.Fatal("session survived revoke-all")
	}
}

func TestServeHealthcheckBackup(t *testing.T) {
	t.Parallel()
	vars := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrCh := make(chan net.Addr, 1)
	var wg sync.WaitGroup
	var served run
	wg.Go(func() { served = execute(t, ctx, vars, func(a net.Addr) { addrCh <- a }) })
	var addr net.Addr
	select {
	case addr = <-addrCh:
	case <-time.After(10 * time.Second):
		t.Fatal("server did not start")
	}

	hc := map[string]string{"VELA_LISTEN": addr.String()}
	if r := execute(t, context.Background(), hc, nil, "healthcheck"); r.err != nil {
		t.Fatalf("healthcheck: %v", r.err)
	}

	// A key created by a second process works immediately on the server.
	kr := execute(t, context.Background(), vars, nil, "key", "create", "--name", "a", "--scope", "admin")
	if kr.err != nil {
		t.Fatal(kr.err)
	}
	req, _ := http.NewRequest("PUT", "http://"+addr.String()+"/api/v1/content/docs/a.txt", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(kr.stdout))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d", res.StatusCode)
	}
	dest := filepath.Join(t.TempDir(), "backup")
	r := execute(t, context.Background(), vars, nil, "backup", dest)
	if r.err != nil || !strings.Contains(r.stdout, "1 blob(s): 1 linked") {
		t.Fatalf("backup = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dest, "vela.db")); err != nil {
		t.Fatal(err)
	}
	if r := execute(t, context.Background(), vars, nil, "backup", dest); r.err == nil {
		t.Fatal("backup into a non-empty directory succeeded")
	}

	cancel()
	wg.Wait()
	if served.err != nil {
		t.Fatalf("serve returned %v\n%s", served.err, served.stderr)
	}
	if !strings.Contains(served.stderr, "velad started") || !strings.Contains(served.stderr, "velad stopped") {
		t.Fatalf("serve log = %s", served.stderr)
	}
	if r := execute(t, context.Background(), hc, nil, "healthcheck"); r.err == nil {
		t.Fatal("healthcheck passed after shutdown")
	}
}

func TestHealthURL(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080/healthz",
		"0.0.0.0:80":     "http://127.0.0.1:80/healthz",
		"[::]:8080":      "http://[::1]:8080/healthz",
		"127.0.0.1:9000": "http://127.0.0.1:9000/healthz",
	} {
		if got, err := healthURL(in); err != nil || got != want {
			t.Errorf("healthURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := healthURL("nonsense"); err == nil {
		t.Error("bad listen address accepted")
	}
}

func TestServeWebSignIn(t *testing.T) {
	t.Parallel()
	vars := newEnv(t)
	vars["VELA_WEB_AUTH"] = "on"
	vars["VELA_WEB_INSECURE_COOKIE"] = "on"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrCh := make(chan net.Addr, 1)
	var wg sync.WaitGroup
	var served run
	wg.Go(func() { served = execute(t, ctx, vars, func(a net.Addr) { addrCh <- a }) })
	var addr net.Addr
	select {
	case addr = <-addrCh:
	case <-time.After(10 * time.Second):
		t.Fatal("server did not start")
	}
	res, err := http.Get("http://" + addr.String() + "/_vela/login")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("sign-in page status = %d", res.StatusCode)
	}
	cancel()
	wg.Wait()
	if served.err != nil || !strings.Contains(served.stderr, "web sign-in enabled") || !strings.Contains(served.stderr, "without Secure") {
		t.Fatalf("serve = %v\n%s", served.err, served.stderr)
	}
}
