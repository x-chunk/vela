package cli

import (
	"os"
	"path/filepath"
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
