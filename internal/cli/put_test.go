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
	"time"

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
		{[]string{f, "docs/math/"}, false, false, "docs/math/report.pdf"},
		{[]string{f, "/docs/math/q1.pdf"}, false, false, "docs/math/q1.pdf"},
		{[]string{f, g, "docs/"}, false, false, "docs/report.pdf docs/b.pdf"},
		{[]string{"-", "notes/x.txt"}, false, false, "notes/x.txt"},
		{[]string{tree, "docs/dir/"}, true, false, "docs/dir/a.txt docs/dir/sub/b.txt"},
		{[]string{tree, "docs/dir/"}, true, true, "docs/dir/.git/config docs/dir/.hidden docs/dir/a.txt docs/dir/sub/b.txt"},
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
		{f, g, "docs/q.pdf"}, // several sources need a directory
		{tree, "docs/"},      // directory without -r
		{"-", "docs/"},       // stdin needs a name
		{"-", "-", "x"},      // stdin twice
		{f, "api/x.pdf"},     // reserved path
		{filepath.Join(h.dir, "missing"), "x"},
		{f, filepath.Join(h.dir, "report.pdf"), "docs/"}, // duplicate destination
	}
	for _, args := range bad {
		if _, err := a.planPut(args, false, false); err == nil || exitCode(err) != exitUsage {
			t.Errorf("planPut(%v) = %v; want a usage error", args, err)
		}
	}
	if _, err := a.planPut([]string{tree, "docs/x"}, true, false); err == nil {
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
		if name != "myeditor" || args[0] != "--wait" || !interactive || filepath.Base(args[1]) != "c.md" {
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

// TestNewEditLongName covers R-027 for the editor: a remote name at the
// 255-byte limit still gets a usable temporary file, with its extension.
func TestNewEditLongName(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	name := strings.Repeat("n", 252) + ".md"
	var edited string
	h.run = func(_ context.Context, _ io.Reader, _ bool, _ string, args ...string) error {
		edited = args[len(args)-1]
		return os.WriteFile(edited, []byte("# long\n"), 0o600)
	}
	h.ok("new", "notes/"+name, "--edit")
	if filepath.Base(edited) != "content.md" {
		t.Fatalf("editor file = %s", edited)
	}
	if _, err := os.Stat(filepath.Dir(edited)); !os.IsNotExist(err) {
		t.Fatalf("temporary directory left behind: %v", err)
	}
	if editName("short.txt") != "short.txt" || editName(strings.Repeat("x", 100)+".tar.verylongextension") != "content" {
		t.Fatal("editName")
	}
}

type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

type blocking struct{ ch chan struct{} }

func (b blocking) Read([]byte) (int, error) { <-b.ch; return 0, io.EOF }

// TestNewBoundsInput covers R-028: input beyond the server's limit fails
// without being buffered whole, and --timeout covers waiting for input and
// for the editor.
func TestNewBoundsInput(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	r := h.execIn(endless{}, "new", "big.txt", "--stdin")
	if r.code != exitError || !strings.Contains(r.stderr, "limit") || !strings.Contains(r.stderr, "vela put") {
		t.Fatalf("endless stdin = %+v", r)
	}
	stuck := blocking{ch: make(chan struct{})}
	defer close(stuck.ch)
	start := time.Now()
	r = h.execIn(stuck, "new", "slow.txt", "--stdin", "--timeout", "200ms")
	if r.code == 0 || !strings.Contains(r.stderr, "timed out") || time.Since(start) > 5*time.Second {
		t.Fatalf("blocked stdin = %+v after %v", r, time.Since(start))
	}
	var editorDeadline bool
	h.run = func(ctx context.Context, _ io.Reader, _ bool, _ string, _ ...string) error {
		_, editorDeadline = ctx.Deadline()
		<-ctx.Done()
		return ctx.Err()
	}
	r = h.exec("", "new", "edit.md", "--edit", "--timeout", "200ms")
	if r.code == 0 || !editorDeadline || !strings.Contains(r.stderr, "timed out") {
		t.Fatalf("hung editor = %+v (deadline %v)", r, editorDeadline)
	}
	h.run = func(_ context.Context, _ io.Reader, _ bool, _ string, args ...string) error {
		return os.WriteFile(args[len(args)-1], []byte(strings.Repeat("b", 65<<10)), 0o600)
	}
	if r := h.exec("", "new", "huge.md", "--edit"); r.code != exitError || !strings.Contains(r.stderr, "limit") {
		t.Fatalf("oversized editor output = %+v", r)
	}
	if r := h.exec("", "new", "c.txt", "--content", strings.Repeat("c", 65<<10)); r.code != exitError || !strings.Contains(r.stderr, "limit") {
		t.Fatalf("oversized --content = %+v", r)
	}
}
