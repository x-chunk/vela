package cli

import (
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
