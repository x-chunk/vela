package cli

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

func TestManage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("web/index.html", "<h1>hi</h1>", api.Unlisted)
	h.seed("web/css/a.css", "a{}", api.Unlisted)

	h.ok("mv", "web/index.html", "web/home.html")
	h.ok("mv", "web/", "site/")
	h.ok("mv", "site/home.html", "archive/")
	var o api.Object
	h.ok("stat", "archive/home.html", "--json").json(t, &o)
	h.seed("taken.html", "t", api.Public)
	if r := h.exec("", "mv", "archive/home.html", "taken.html"); r.code != exitConflict || !strings.Contains(r.stderr, "--force") {
		t.Fatalf("mv onto an existing file = %+v", r)
	}
	h.ok("mv", "archive/home.html", "taken.html", "--force")

	h.ok("vis", "-r", "site/", "public")
	h.ok("stat", "site/css/a.css", "--json").json(t, &o)
	if o.Visibility != api.Public {
		t.Fatalf("vis -r did not apply: %+v", o)
	}
	h.ok("vis", "taken.html", "private")
	if r := h.exec("", "vis", "site/", "public"); r.code != exitUsage {
		t.Fatalf("vis on a directory without -r = %+v", r)
	}
	if r := h.exec("", "vis", "taken.html", "secret"); r.code != exitUsage {
		t.Fatalf("vis with a bad visibility = %+v", r)
	}

	h.ok("meta", "site/css/a.css", "--title", "Styles", "--tag", "web", "--tag", "css", "--untag", "css", "--expires", "7d")
	h.ok("meta", "site/css/a.css", "--json").json(t, &o)
	if o.Title != "Styles" || strings.Join(o.Tags, ",") != "web" || o.ExpiresAt == nil {
		t.Fatalf("meta = %+v", o)
	}
	h.ok("meta", "site/css/a.css", "--expires", "none", "--disposition", "attachment", "--json").json(t, &o)
	if o.ExpiresAt != nil || o.Disposition != api.Attachment {
		t.Fatalf("meta reset = %+v", o)
	}

	if r := h.ok("rm", "taken.html"); !strings.Contains(r.stdout, "trash") {
		t.Fatalf("rm = %q", r.stdout)
	}
	if r := h.exec("", "rm", "site/"); r.code != exitUsage {
		t.Fatalf("rm dir/ without -r = %+v", r)
	}
	if r := h.exec("", "rm", "-r", "site/"); r.code != exitUsage || !strings.Contains(r.stderr, "-y") {
		t.Fatalf("unconfirmed rm in a script = %+v", r)
	}
	h.ok("rm", "-r", "-y", "site/")
	if r := h.exec("", "rm", "nothing.txt", "neither.txt"); r.code != exitPartial {
		t.Fatalf("rm of missing files = %+v", r)
	}
}

func TestExitCodes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("f.txt", "x", api.Unlisted)
	if r := h.exec("", "stat", "missing.txt"); r.code != exitNotFound {
		t.Errorf("not found: %d", r.code)
	}
	if r := h.exec("", "stat", "api/x"); r.code != exitUsage {
		t.Errorf("invalid path: %d", r.code)
	}
	if r := h.exec("", "ls", "--nope"); r.code != exitUsage {
		t.Errorf("unknown flag: %d", r.code)
	}
	if r := h.exec("", "stat"); r.code != exitUsage {
		t.Errorf("missing argument: %d", r.code)
	}
	if r := h.exec("", "frobnicate"); r.code != exitUsage {
		t.Errorf("unknown command: %d", r.code)
	}
	if r := h.exec("", "mv", "f.txt", "f.txt/inner", "--json"); r.code != 0 {
		t.Errorf("mv into own name: %+v", r)
	}

	ro := h.ts.Key(t, "readonly", api.ScopeRO, "")
	h.login(ro)
	if r := h.exec("", "mv", "f.txt/inner", "g.txt"); r.code != exitAuth {
		t.Errorf("forbidden: %d", r.code)
	}
	h.login("vela_aaaaaaaaaa_" + strings.Repeat("A", 43))
	if r := h.exec("", "ls"); r.code != exitAuth || !strings.Contains(r.stderr, "vela login") {
		t.Errorf("unauthorized: %+v", r)
	}

	dead := httptest.NewServer(nil)
	url := dead.URL
	dead.Close()
	h.env["VELA_URL"] = url
	if r := h.exec("", "ls"); r.code != exitNetwork {
		t.Errorf("network: %+v", r)
	}
	delete(h.env, "VELA_URL")

	h.config = filepath.Join(t.TempDir(), "none.toml")
	if r := h.exec("", "ls"); r.code != exitAuth || !strings.Contains(r.stderr, "not logged in") {
		t.Errorf("not logged in: %+v", r)
	}
}

// TestVisTargets covers R-023 and R-024: -r applies to files too, missing
// paths are errors, and repeated or nested targets are changed once.
func TestVisTargets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("file.txt", "x", api.Public)
	h.seed("dir/a.txt", "a", api.Public)
	h.seed("dir/sub/b.txt", "b", api.Public)
	stat := func(p string) api.Object {
		var o api.Object
		h.ok("stat", p, "--json").json(t, &o)
		return o
	}
	h.ok("vis", "-r", "file.txt", "private")
	if stat("file.txt").Visibility != api.Private {
		t.Fatal("vis -r on a file did nothing")
	}
	if r := h.exec("", "vis", "-r", "missing", "private"); r.code != exitNotFound {
		t.Fatalf("vis -r on a missing path = %+v", r)
	}
	if r := h.exec("", "vis", "-r", "missing/", "private"); r.code != exitNotFound {
		t.Fatalf("vis -r on a missing directory = %+v", r)
	}
	if r := h.exec("", "vis", "file.txt", "nothing.txt", "public"); r.code != exitNotFound || stat("file.txt").Visibility != api.Private {
		t.Fatalf("a missing target must stop the command before any change: %+v", r)
	}
	r := h.ok("vis", "file.txt", "file.txt", "unlisted")
	if !strings.Contains(r.stdout, "1 object(s)") {
		t.Fatalf("duplicate target = %q", r.stdout)
	}
	var objs []api.Object
	h.ok("vis", "-r", "dir/", "dir/sub/", "dir", "file.txt", "private", "--json").json(t, &objs)
	if len(objs) != 3 {
		t.Fatalf("nested and mixed targets = %d objects", len(objs))
	}
}
