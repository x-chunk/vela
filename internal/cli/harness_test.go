package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/cli/config"
	"go.xchunk.org/vela/internal/cli/output"
	"go.xchunk.org/vela/internal/testutil"
)

// harness runs CLI commands in-process against an in-process server.
type harness struct {
	t      *testing.T
	ts     *testutil.Server
	dir    string
	config string
	env    map[string]string
	// run overrides App.Run (browser, clipboard, editor).
	run func(ctx context.Context, stdin io.Reader, interactive bool, name string, args ...string) error
	// onRequest, if set, sees every HTTP request the CLI sends.
	onRequest func(*http.Request)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type result struct {
	code           int
	stdout, stderr string
}

func (r result) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.stdout), v); err != nil {
		t.Fatalf("stdout is not JSON (%v):\n%s\nstderr: %s", err, r.stdout, r.stderr)
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ts := testutil.NewServer(t)
	dir := t.TempDir()
	h := &harness{t: t, ts: ts, dir: dir, config: filepath.Join(dir, "config", "config.toml"), env: map[string]string{}}
	h.login(ts.AdminKey)
	return h
}

func (h *harness) login(key string) {
	h.t.Helper()
	f := &config.File{DefaultProfile: "main", Profiles: map[string]*config.Profile{"main": {URL: h.ts.URL, Key: key}}}
	if err := f.Save(h.config); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) exec(stdin string, args ...string) result {
	h.t.Helper()
	return h.execIn(strings.NewReader(stdin), args...)
}

func (h *harness) execIn(stdin io.Reader, args ...string) result {
	h.t.Helper()
	var out, errOut bytes.Buffer
	env := map[string]string{"VELA_CONFIG": h.config}
	for k, v := range h.env {
		env[k] = v
	}
	a := &App{
		In:            stdin,
		Out:           &out,
		Err:           &errOut,
		LookupEnv:     func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		InIsTerminal:  func() bool { return false },
		OutIsTerminal: func() bool { return false },
		ErrIsTerminal: func() bool { return false },
		ReadSecret:    func() (string, error) { return "", fmt.Errorf("no terminal") },
		Run: func(ctx context.Context, stdin io.Reader, interactive bool, name string, args ...string) error {
			if h.run != nil {
				return h.run(ctx, stdin, interactive, name, args...)
			}
			return fmt.Errorf("unexpected program %s", name)
		},
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if h.onRequest != nil {
				h.onRequest(r)
			}
			return http.DefaultTransport.RoundTrip(r)
		})},
		Now:          time.Now,
		RetryBackoff: func(int) time.Duration { return time.Millisecond },
	}
	code := a.Execute(context.Background(), args)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func (h *harness) ok(args ...string) result {
	h.t.Helper()
	r := h.exec("", args...)
	if r.code != 0 {
		h.t.Fatalf("vela %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r
}

// seed uploads content directly through the Go client, so tests of one
// command do not depend on other commands.
func (h *harness) seed(path, content string, vis api.Visibility) {
	h.t.Helper()
	c, err := client.New(h.ts.URL, h.ts.AdminKey)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, _, err := c.Upload(context.Background(), path, strings.NewReader(content), client.UploadOptions{Size: int64(len(content)), Visibility: vis}); err != nil {
		h.t.Fatal(err)
	}
}

func newTestPrinter() *output.Printer { return &output.Printer{Out: io.Discard, Err: io.Discard} }

func (h *harness) file(name, content string) string {
	h.t.Helper()
	p := filepath.Join(h.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return p
}
