package cli

import (
	"strings"
	"testing"
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
