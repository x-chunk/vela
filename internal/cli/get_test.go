package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

func TestGetAndCat(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("archive/home.html", "<h1>hi</h1>", api.Private)
	h.seed("site/css/a.css", "a{}", api.Public)
	h.seed("site/index.html", "i", api.Public)

	if r := h.ok("cat", "archive/home.html"); r.stdout != "<h1>hi</h1>" {
		t.Fatalf("cat = %q", r.stdout)
	}
	if r := h.ok("get", "archive/home.html", "-"); r.stdout != "<h1>hi</h1>" {
		t.Fatalf("get - = %q", r.stdout)
	}
	out := filepath.Join(h.dir, "dl")
	h.ok("get", "archive/home.html", out)
	if b, _ := os.ReadFile(out); string(b) != "<h1>hi</h1>" {
		t.Fatalf("get wrote %q", b)
	}
	if r := h.exec("", "get", "archive/home.html", out); r.code != exitConflict || !strings.Contains(r.stderr, "--force") {
		t.Fatalf("get over an existing file = %+v", r)
	}
	h.ok("get", "archive/home.html", out, "--force")
	dir := filepath.Join(h.dir, "into")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.ok("get", "archive/home.html", dir)
	if _, err := os.Stat(filepath.Join(dir, "home.html")); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(h.dir, "tree")
	var objs []api.Object
	h.ok("get", "-r", "site/", tree, "--json").json(t, &objs)
	if b, _ := os.ReadFile(filepath.Join(tree, "css", "a.css")); string(b) != "a{}" || len(objs) != 2 {
		t.Fatalf("get -r wrote %q (%d objects)", b, len(objs))
	}
	if r := h.exec("", "get", "site"); r.code != exitNotFound || !strings.Contains(r.stderr, "get -r site/") {
		t.Fatalf("get of a directory = %+v", r)
	}
	if r := h.exec("", "get", "site/"); r.code != exitUsage {
		t.Fatalf("get dir/ without -r = %+v", r)
	}
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".vela-") {
			t.Fatalf("temporary download file left: %s", e.Name())
		}
	}
}

// TestGetTreeStaysInsideLocal covers R-005: a symlink inside LOCAL pointing
// elsewhere must not let get -r write outside LOCAL, with or without force.
func TestGetTreeStaysInsideLocal(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	h := newHarness(t)
	h.seed("tree/sub/payload", "evil", api.Unlisted)
	h.seed("tree/ok.txt", "fine", api.Unlisted)
	local := filepath.Join(h.dir, "local")
	outside := filepath.Join(h.dir, "outside")
	for _, d := range []string{local, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(local, "sub")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"get", "-r", "tree/", local}, {"get", "-r", "tree/", local, "--force"}} {
		r := h.exec("", args...)
		if r.code != exitPartial {
			t.Fatalf("%v: exit %d, stderr %s", args, r.code, r.stderr)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Fatalf("%v wrote outside LOCAL: %v", args, entries)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(local, "ok.txt")); string(b) != "fine" {
		t.Fatalf("ok.txt = %q", b)
	}
}

// TestGetNeverReplacesWithoutForce covers R-006: a local file created while
// the download runs survives, and the command fails.
func TestGetNeverReplacesWithoutForce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("doc.txt", "REMOTE", api.Unlisted)
	local := filepath.Join(h.dir, "doc.txt")
	h.onRequest = func(r *http.Request) {
		if strings.HasPrefix(r.URL.Path, api.RouteCont+"/") {
			_ = os.WriteFile(local, []byte("LOCAL"), 0o644)
		}
	}
	r := h.exec("", "get", "doc.txt", local)
	if r.code != exitConflict {
		t.Fatalf("exit %d, stderr %s", r.code, r.stderr)
	}
	if b, _ := os.ReadFile(local); string(b) != "LOCAL" {
		t.Fatalf("local file replaced: %q", b)
	}
	h.ok("get", "doc.txt", local, "--force")
	if b, _ := os.ReadFile(local); string(b) != "REMOTE" {
		t.Fatalf("--force did not replace: %q", b)
	}
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".vela-") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
}

// TestGetLongName covers R-027: a remote name at the 255-byte limit can be
// downloaded under its own name.
func TestGetLongName(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	name := strings.Repeat("n", 251) + ".txt"
	h.seed("long/"+name, "x", api.Unlisted)
	dir := filepath.Join(h.dir, "dl")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.ok("get", "long/"+name, dir)
	h.ok("get", "-r", "long/", filepath.Join(h.dir, "tree"))
	for _, p := range []string{filepath.Join(dir, name), filepath.Join(h.dir, "tree", name)} {
		if b, err := os.ReadFile(p); err != nil || string(b) != "x" {
			t.Fatalf("%s: %q, %v", p, b, err)
		}
	}
}
