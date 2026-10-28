package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

// seedTags creates the tagged tree the --by-tag tests share.
func seedTags(h *harness) {
	h.t.Helper()
	h.seedTagged("a.txt", "q1")
	h.seedTagged("docs/math.pdf", "math", "draft")
	h.seedTagged("docs/sub/math2.pdf", "math")
	h.seedTagged("docs/plain.txt")
	h.seedTagged("other/q1.txt", "q1")
}

func TestLsByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"ls", "--by-tag", "math"}, "docs/math.pdf docs/sub/math2.pdf"},
		{[]string{"ls", "--by-tags", "math draft"}, "docs/math.pdf"},
		{[]string{"ls", "--by-tag", "Q1", "--by-tags", "draft math"}, "a.txt docs/math.pdf other/q1.txt"},
		{[]string{"ls", "docs/", "--by-tag", "math"}, "math.pdf sub/math2.pdf"},
		{[]string{"ls", "docs/math.pdf", "--by-tag", "math"}, "math.pdf"},
		{[]string{"ls", "docs/plain.txt", "--by-tag", "math"}, ""},
		{[]string{"ls", "--by-tag", "nope"}, ""},
	}
	for _, tt := range tests {
		r := h.ok(tt.args...)
		if got := strings.Join(strings.Fields(r.stdout), " "); got != tt.want {
			t.Errorf("vela %s = %q; want %q", strings.Join(tt.args, " "), got, tt.want)
		}
	}
	for _, bad := range [][]string{{"ls", "--by-tag", "a b"}, {"ls", "--by-tags", " "}, {"ls", "--by-tag", "a/b"}} {
		if r := h.exec("", bad...); r.code != exitUsage {
			t.Errorf("vela %s: exit %d, want %d (%s)", strings.Join(bad, " "), r.code, exitUsage, r.stderr)
		}
	}
}

func TestStatAndURLByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	h.ok("vis", "docs/math.pdf", "public")

	var objs []api.Object
	h.ok("stat", "--by-tag", "math", "--json").json(t, &objs)
	if len(objs) != 2 || objs[0].Path != "docs/math.pdf" || objs[1].Path != "docs/sub/math2.pdf" {
		t.Fatalf("stat --by-tag = %+v", objs)
	}
	if r := h.ok("stat", "--by-tag", "math"); strings.Count(r.stdout, "Path") != 2 {
		t.Errorf("human stat = %s", r.stdout)
	}
	if r := h.exec("", "stat", "--by-tag", "nope"); r.code != exitNotFound {
		t.Errorf("stat of nothing: exit %d", r.code)
	}

	r := h.ok("url", "--by-tag", "math")
	if lines := strings.Fields(r.stdout); len(lines) != 1 || !strings.HasSuffix(lines[0], "/docs/math.pdf") {
		t.Errorf("url --by-tag stdout = %q", r.stdout)
	}
	if !strings.Contains(r.stderr, "docs/sub/math2.pdf is private") {
		t.Errorf("url --by-tag stderr = %q", r.stderr)
	}
	if r := h.exec("", "url", "--by-tag", "q1"); r.code != exitError {
		t.Errorf("url of private selection: exit %d", r.code)
	}
}

func TestOpenByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	h.ok("vis", "docs/math.pdf", "docs/sub/math2.pdf", "public")
	var opened []string
	h.run = func(_ context.Context, _ io.Reader, _ bool, _ string, args ...string) error {
		opened = append(opened, args[len(args)-1])
		return nil
	}

	if r := h.ok("open", "--by-tag", "math", "--dry-run"); len(strings.Fields(r.stdout)) != 2 || len(opened) != 0 {
		t.Fatalf("dry run: stdout %q, opened %v", r.stdout, opened)
	}
	if r := h.exec("", "open", "--by-tag", "math"); r.code != exitUsage || len(opened) != 0 {
		t.Fatalf("no terminal: exit %d, opened %v", r.code, opened)
	}
	h.tty = true
	r := h.exec("n\n", "open", "--by-tag", "math")
	if r.code != exitError || len(opened) != 0 || !strings.Contains(r.stderr, "Open 2 browser tab(s)? [y/N]") ||
		!strings.Contains(r.stderr, "/docs/sub/math2.pdf") {
		t.Fatalf("declined: exit %d, opened %v, stderr %q", r.code, opened, r.stderr)
	}
	if r := h.exec("y\n", "open", "--by-tag", "math"); r.code != 0 || len(opened) != 2 {
		t.Fatalf("accepted: exit %d, opened %v, stderr %q", r.code, opened, r.stderr)
	}
	h.tty = false
	opened = nil
	h.ok("open", "--by-tags", "math draft", "-y")
	if len(opened) != 1 || !strings.HasSuffix(opened[0], "/docs/math.pdf") {
		t.Fatalf("-y opened %v", opened)
	}
}

func TestGetByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(h.dir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	out := filepath.Join(h.dir, "math")
	h.ok("get", "--by-tag", "math", "docs/", out)
	if read("math/math.pdf") != "docs/math.pdf" || read("math/sub/math2.pdf") != "docs/sub/math2.pdf" {
		t.Error("structure below docs/ not kept")
	}
	if _, err := os.Stat(filepath.Join(out, "plain.txt")); err == nil {
		t.Error("untagged file downloaded")
	}
	h.ok("get", "--by-tag", "q1", "/", filepath.Join(h.dir, "q1"))
	if read("q1/a.txt") != "a.txt" || read("q1/other/q1.txt") != "other/q1.txt" {
		t.Error("structure from the root not kept")
	}
	if r := h.exec("", "get", "--by-tag", "math", "docs/", out); r.code == 0 {
		t.Error("existing files replaced without --force")
	}
	h.ok("get", "--by-tag", "math", "docs", out, "--force")
	if r := h.exec("", "get", "--by-tag", "math", "docs/", "-"); r.code != exitUsage {
		t.Errorf("stdout download: exit %d", r.code)
	}
	if r := h.exec("", "get", "--by-tag", "nope", "/", out); r.code != exitNotFound {
		t.Errorf("nothing tagged: exit %d", r.code)
	}
}

func TestMetaAndVisByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	stat := func(p string) api.Object {
		t.Helper()
		var o api.Object
		h.ok("stat", p, "--json").json(t, &o)
		return o
	}

	h.ok("meta", "--by-tag", "math", "--tag", "sorted", "--untag", "draft")
	if got := stat("docs/math.pdf").Tags; strings.Join(got, ",") != "math,sorted" {
		t.Errorf("docs/math.pdf tags = %v", got)
	}
	if got := stat("docs/plain.txt").Tags; len(got) != 0 {
		t.Errorf("untagged file changed: %v", got)
	}
	h.ok("meta", "--by-tag", "q1", "other/", "--description", "only other")
	if stat("a.txt").Description != "" || stat("other/q1.txt").Description != "only other" {
		t.Error("meta --by-tag ignored its scope")
	}
	for _, bad := range [][]string{
		{"meta", "--by-tag", "math", "--title", "x"},
		{"meta", "--by-tag", "math"},
	} {
		if r := h.exec("", bad...); r.code != exitUsage {
			t.Errorf("vela %s: exit %d", strings.Join(bad, " "), r.code)
		}
	}
	if r := h.exec("", "meta", "--by-tag", "nope", "--tag", "x"); r.code != exitNotFound {
		t.Errorf("meta of nothing: exit %d", r.code)
	}

	h.ok("vis", "--by-tags", "math sorted", "public")
	if stat("docs/math.pdf").Visibility != api.Public || stat("docs/sub/math2.pdf").Visibility != api.Public ||
		stat("a.txt").Visibility != api.Private {
		t.Error("vis --by-tags changed the wrong objects")
	}
	h.ok("vis", "--by-tag", "q1", "other/", "unlisted")
	if stat("a.txt").Visibility != api.Private || stat("other/q1.txt").Visibility != api.Unlisted {
		t.Error("vis --by-tag ignored its scope")
	}
}

func TestShareAndLinksByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)

	if r := h.ok("share", "--by-tag", "math", "--dry-run"); strings.Fields(r.stdout)[0] != "docs/math.pdf" {
		t.Fatalf("dry run = %q", r.stdout)
	}
	if r := h.exec("", "share", "--by-tag", "math"); r.code != exitUsage {
		t.Fatalf("no terminal: exit %d", r.code)
	}
	var links []api.Link
	h.ok("share", "--by-tag", "math", "--ttl", "2h", "--note", "batch", "-y", "--json").json(t, &links)
	if len(links) != 2 || links[0].Path != "docs/math.pdf" || links[0].Note != "batch" {
		t.Fatalf("share --by-tag = %+v", links)
	}
	h.ok("share", "a.txt")
}
