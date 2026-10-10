package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

func TestPlanPut(t *testing.T) {
	t.Parallel()
	h := &harness{t: t, dir: t.TempDir()}
	a := &App{Err: io.Discard}
	f := h.file("report.pdf", "x")
	g := h.file("b.pdf", "y")
	h.file("tree/a.txt", "a")
	h.file("tree/sub/b.txt", "b")
	h.file("tree/.hidden", "h")
	h.file("tree/.git/config", "h")
	tree := filepath.Join(h.dir, "tree")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(f, filepath.Join(tree, "link.pdf")); err != nil {
			t.Fatal(err)
		}
	}
	a.printer = newTestPrinter()

	tests := []struct {
		args      []string
		recursive bool
		hidden    bool
		want      string
	}{
		{[]string{f}, false, false, "report.pdf"},
		{[]string{f, "edu/math/"}, false, false, "edu/math/report.pdf"},
		{[]string{f, "/edu/math/q1.pdf"}, false, false, "edu/math/q1.pdf"},
		{[]string{f, g, "edu/"}, false, false, "edu/report.pdf edu/b.pdf"},
		{[]string{"-", "notes/x.txt"}, false, false, "notes/x.txt"},
		{[]string{tree, "edu/dir/"}, true, false, "edu/dir/a.txt edu/dir/sub/b.txt"},
		{[]string{tree, "edu/dir/"}, true, true, "edu/dir/.git/config edu/dir/.hidden edu/dir/a.txt edu/dir/sub/b.txt"},
	}
	for _, tt := range tests {
		plan, err := a.planPut(tt.args, tt.recursive, tt.hidden)
		if err != nil {
			t.Errorf("planPut(%v): %v", tt.args, err)
			continue
		}
		var got []string
		for _, it := range plan {
			got = append(got, it.Remote)
		}
		if strings.Join(got, " ") != tt.want {
			t.Errorf("planPut(%v) = %q; want %q", tt.args, strings.Join(got, " "), tt.want)
		}
	}

	bad := [][]string{
		{f, g, "edu/q.pdf"}, // several sources need a directory
		{tree, "edu/"},      // directory without -r
		{"-", "edu/"},       // stdin needs a name
		{"-", "-", "x"},     // stdin twice
		{f, "api/x.pdf"},    // reserved path
		{filepath.Join(h.dir, "missing"), "x"},
		{f, filepath.Join(h.dir, "report.pdf"), "edu/"}, // duplicate destination
	}
	for _, args := range bad {
		if _, err := a.planPut(args, false, false); err == nil || exitCode(err) != exitUsage {
			t.Errorf("planPut(%v) = %v; want a usage error", args, err)
		}
	}
	if _, err := a.planPut([]string{tree, "edu/x"}, true, false); err == nil {
		t.Error("directory into a file path accepted")
	}
}

func TestPutJSONAndExitCodes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.file("a.txt", "alpha")
	b := h.file("b.txt", "beta")

	r := h.ok("put", a, b, "docs/", "--public", "--title", "T", "--tag", "x", "--json")
	var objs []api.Object
	r.json(t, &objs)
	if len(objs) != 2 || objs[0].Path != "docs/a.txt" || objs[0].Title != "T" || objs[0].Visibility != api.Public || objs[0].URL == nil {
		t.Fatalf("put --json = %+v", objs)
	}

	r = h.exec("", "put", a, "docs/", "--json")
	if r.code != exitConflict {
		t.Fatalf("existing file: exit %d, %s", r.code, r.stdout)
	}
	var e api.ErrorResponse
	r.json(t, &e)
	if e.Error.Code != api.CodeAlreadyExists || e.Error.Details["hint"] != "use --force to overwrite" {
		t.Fatalf("error JSON = %+v", e.Error)
	}

	r = h.exec("", "put", a, "docs/")
	if r.code != exitConflict || !strings.Contains(r.stderr, "use --force to overwrite") || r.stdout != "" {
		t.Fatalf("human conflict = %+v", r)
	}

	c := h.file("c.txt", "gamma")
	r = h.exec("", "put", a, c, "docs/", "--json")
	if r.code != exitPartial {
		t.Fatalf("partial: exit %d %s", r.code, r.stdout)
	}
	r.json(t, &e)
	if e.Error.Code != "partial_failure" {
		t.Fatalf("partial JSON = %+v", e.Error)
	}
	h.ok("put", a, "docs/", "--force")

	r = h.exec("piped content", "put", "-", "notes/piped.txt", "--json")
	var objs2 []api.Object
	r.json(t, &objs2)
	if r.code != 0 || objs2[0].Size != int64(len("piped content")) {
		t.Fatalf("stdin upload = %+v", r)
	}

	r = h.ok("put", a, "dry/", "--dry-run", "--json")
	var plan []putItem
	r.json(t, &plan)
	if len(plan) != 1 || plan[0].Remote != "dry/a.txt" {
		t.Fatalf("dry run = %+v", plan)
	}
	if r := h.exec("", "stat", "dry/a.txt"); r.code != exitNotFound {
		t.Fatalf("dry run uploaded something: %+v", r)
	}
	if r := h.exec("", "put", h.file("x", "x"), "--public", "--private"); r.code != exitUsage {
		t.Errorf("exclusive flags: %d", r.code)
	}
}

func TestNewSources(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.ok("new", "a.md", "--content", "# A")
	if r := h.exec("from stdin", "new", "b.txt"); r.code != 0 {
		t.Fatalf("new from piped stdin = %+v", r)
	}
	if r := h.ok("cat", "b.txt"); r.stdout != "from stdin" {
		t.Fatalf("cat = %q", r.stdout)
	}
	h.env["EDITOR"] = "myeditor --wait"
	h.run = func(_ context.Context, _ io.Reader, interactive bool, name string, args ...string) error {
		if name != "myeditor" || args[0] != "--wait" || !interactive || !strings.HasSuffix(args[1], "-c.md") {
			return fmt.Errorf("unexpected editor call %s %v", name, args)
		}
		return os.WriteFile(args[1], []byte("# from editor\n"), 0o600)
	}
	h.ok("new", "c.md", "--edit")
	if r := h.ok("cat", "c.md"); r.stdout != "# from editor\n" {
		t.Fatalf("cat = %q", r.stdout)
	}
	h.run = func(_ context.Context, _ io.Reader, _ bool, _ string, args ...string) error { return nil }
	if r := h.exec("", "new", "d.md", "--edit"); r.code != exitError || !strings.Contains(r.stderr, "empty") {
		t.Fatalf("empty editor buffer = %+v", r)
	}
	if r := h.exec("", "new", "e.md", "--content", "x", "--stdin"); r.code != exitUsage {
		t.Fatalf("two sources = %+v", r)
	}
	var o api.Object
	h.exec("\x00\x01binary", "new", "bin.dat", "--stdin", "--json").json(t, &o)
	if o.Size != 8 {
		t.Fatalf("binary content = %+v", o)
	}
}
