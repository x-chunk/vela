package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

func TestBrowse(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("web/index.html", "<h1>hi</h1>", api.Unlisted)
	h.seed("web/css/a.css", "a{}", api.Public)
	h.seed("secret.txt", "s", api.Private)

	if r := h.ok("ls", "web/"); r.stdout != "css/\nindex.html\n" {
		t.Fatalf("ls = %q", r.stdout)
	}
	if r := h.ok("ls"); r.stdout != "web/\nsecret.txt\n" {
		t.Fatalf("ls root = %q", r.stdout)
	}
	var l api.Listing
	h.ok("ls", "-r", "--json").json(t, &l)
	if len(l.Objects) != 3 {
		t.Fatalf("ls -r --json = %+v", l)
	}
	h.ok("ls", "-r", "--visibility", "private", "--json").json(t, &l)
	if len(l.Objects) != 1 {
		t.Fatalf("ls --visibility = %+v", l)
	}
	if r := h.ok("ls", "web/index.html"); r.stdout != "index.html\n" {
		t.Fatalf("ls on a file = %q", r.stdout)
	}
	if r := h.ok("ls", "-l", "web/"); !strings.Contains(r.stdout, "unlisted") || !strings.Contains(r.stdout, "11 B") {
		t.Fatalf("ls -l = %q", r.stdout)
	}
	if r := h.ok("stat", "web/index.html"); !strings.Contains(r.stdout, "text/html; charset=utf-8") {
		t.Fatalf("stat = %q", r.stdout)
	}
	if r := h.exec("", "stat", "web"); r.code != exitNotFound || !strings.Contains(r.stderr, "is a directory") {
		t.Fatalf("stat on a directory = %+v", r)
	}
	if r := h.ok("url", "web/css/a.css"); strings.TrimSpace(r.stdout) != h.ts.URL+"/web/css/a.css" {
		t.Fatalf("url = %q", r.stdout)
	}
	if r := h.exec("", "url", "secret.txt"); r.code != exitError || !strings.Contains(r.stderr, "vela share secret.txt") {
		t.Fatalf("url of a private file = %+v", r)
	}
}

func TestOpen(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("pub.txt", "x", api.Public)
	var opened []string
	h.run = func(_ context.Context, _ io.Reader, _ bool, _ string, args ...string) error {
		opened = append(opened, args[len(args)-1])
		return nil
	}
	h.ok("open", "pub.txt")
	h.ok("open", "docs/")
	if len(opened) != 2 || opened[0] != h.ts.URL+"/pub.txt" || opened[1] != h.ts.URL+"/docs/" {
		t.Fatalf("opened = %v", opened)
	}
}

func TestRemoteCompletion(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("edu/math/q1.pdf", "x", api.Unlisted)
	h.seed("edu/readme.md", "x", api.Unlisted)
	h.seed("top.txt", "x", api.Unlisted)
	r := h.ok("__complete", "stat", "edu/")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if lines[0] != "edu/math/" || lines[1] != "edu/readme.md" || !strings.HasPrefix(lines[len(lines)-1], ":") {
		t.Fatalf("completion = %q", r.stdout)
	}
	r = h.ok("__complete", "stat", "t")
	if !strings.HasPrefix(r.stdout, "top.txt\n") {
		t.Fatalf("completion = %q", r.stdout)
	}
	// A dead server yields no suggestions and no noise.
	h.env["VELA_URL"] = "http://127.0.0.1:1"
	r = h.ok("__complete", "stat", "")
	if strings.TrimSpace(r.stdout) != ":4" || strings.Contains(r.stderr, "vela:") {
		t.Fatalf("completion against a dead server = %+v", r)
	}
}
