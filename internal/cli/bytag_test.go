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

	var list api.LinkList
	h.ok("links", "ls", "--by-tag", "math", "--json").json(t, &list)
	if len(list.Links) != 2 {
		t.Fatalf("links ls --by-tag = %+v", list)
	}
	h.ok("links", "ls", "--by-tag", "math", "docs/sub/", "--json").json(t, &list)
	if len(list.Links) != 1 || list.Links[0].Path != "docs/sub/math2.pdf" {
		t.Fatalf("links ls --by-tag docs/sub/ = %+v", list)
	}

	h.tty = true
	if r := h.exec("n\n", "links", "revoke", "--by-tags", "math draft"); r.code != exitError {
		t.Fatalf("declined revoke: exit %d", r.code)
	}
	h.tty = false
	h.ok("links", "revoke", "--by-tags", "math draft", "-y")
	h.ok("links", "ls", "--json").json(t, &list)
	if len(list.Links) != 2 {
		t.Fatalf("after revoke: %+v", list)
	}
	if r := h.exec("", "links", "revoke", "--by-tags", "math draft", "-y"); r.code != exitNotFound {
		t.Errorf("revoke of nothing: exit %d", r.code)
	}
}

func TestRmByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	exists := func(p string) bool { return h.exec("", "stat", p).code == 0 }

	if r := h.ok("rm", "--by-tag", "math", "--dry-run"); strings.Join(strings.Fields(r.stdout), " ") != "docs/math.pdf docs/sub/math2.pdf" ||
		!exists("docs/math.pdf") {
		t.Fatalf("dry run = %q", r.stdout)
	}
	if r := h.exec("", "rm", "--by-tag", "math"); r.code != exitUsage || !exists("docs/math.pdf") {
		t.Fatalf("no terminal: exit %d", r.code)
	}
	h.tty = true
	r := h.exec("n\n", "rm", "--by-tag", "math")
	if r.code != exitError || !exists("docs/math.pdf") || !strings.Contains(r.stderr, "Move 2 object(s) to the trash? [y/N]") {
		t.Fatalf("declined: exit %d, stderr %q", r.code, r.stderr)
	}
	if r := h.exec("y\n", "rm", "--by-tags", "math draft"); r.code != 0 || exists("docs/math.pdf") || !exists("docs/sub/math2.pdf") {
		t.Fatalf("accepted: exit %d, stderr %q", r.code, r.stderr)
	}
	h.tty = false
	h.ok("rm", "--by-tag", "q1", "other/", "-y")
	if exists("other/q1.txt") || !exists("a.txt") {
		t.Error("rm --by-tag ignored its scope")
	}
	if r := h.exec("", "rm", "--by-tag", "q1", "--broken", "-y"); r.code != exitUsage {
		t.Errorf("--broken with --by-tag: exit %d", r.code)
	}
	if r := h.exec("", "rm", "a.txt", "--dry-run"); r.code != exitUsage {
		t.Errorf("--dry-run without --by-tag: exit %d", r.code)
	}
}

func TestMvByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	exists := func(p string) bool { return h.exec("", "stat", p).code == 0 }

	r := h.ok("mv", "--by-tag", "math", "archive/", "--dry-run")
	if got := strings.TrimSpace(r.stdout); got != "docs/math.pdf -> archive/math.pdf\ndocs/sub/math2.pdf -> archive/math2.pdf" {
		t.Fatalf("flat dry run = %q", got)
	}
	r = h.ok("mv", "--by-tag", "math", "docs/", "archive/", "--save-structure", "--dry-run")
	if got := strings.TrimSpace(r.stdout); got != "docs/math.pdf -> archive/math.pdf\ndocs/sub/math2.pdf -> archive/sub/math2.pdf" {
		t.Fatalf("structured dry run = %q", got)
	}
	if r := h.exec("", "mv", "--by-tag", "math", "archive/"); r.code != exitUsage || !exists("docs/math.pdf") {
		t.Fatalf("no terminal: exit %d", r.code)
	}

	// Two q1 files share no name, but "a.txt" collides with an existing file:
	// nothing moves, and every conflict is reported.
	h.seedTagged("dest/a.txt")
	h.seedTagged("x/q1.txt", "q1")
	r = h.exec("", "mv", "--by-tag", "q1", "dest/", "-y")
	if r.code != exitConflict || !strings.Contains(r.stderr, "a.txt -> dest/a.txt: the destination exists") ||
		!strings.Contains(r.stderr, "would both move to dest/q1.txt") || !exists("a.txt") || !exists("other/q1.txt") {
		t.Fatalf("conflicts: exit %d, stderr %q", r.code, r.stderr)
	}
	// --save-structure avoids the name clash; --force replaces dest/a.txt.
	h.ok("mv", "--by-tag", "q1", "dest/", "--save-structure", "--force", "-y")
	for _, p := range []string{"dest/a.txt", "dest/other/q1.txt", "dest/x/q1.txt"} {
		if !exists(p) {
			t.Errorf("%s missing after the move", p)
		}
	}
	if exists("a.txt") || exists("x/q1.txt") {
		t.Error("sources left behind")
	}

	h.tty = true
	if r := h.exec("y\n", "mv", "--by-tags", "math draft", "archive/"); r.code != 0 || !exists("archive/math.pdf") {
		t.Fatalf("confirmed move: exit %d, stderr %q", r.code, r.stderr)
	}
	h.tty = false
	if r := h.ok("mv", "--by-tags", "math draft", "archive/"); !strings.Contains(r.stderr, "already in place") {
		t.Errorf("no-op move: stderr %q", r.stderr)
	}
	if r := h.exec("", "mv", "a", "b", "--save-structure"); r.code != exitUsage {
		t.Errorf("--save-structure without --by-tag: exit %d", r.code)
	}
}

func TestTrashLsByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	h.ok("rm", "--by-tag", "math", "--by-tag", "q1", "-y")
	var list api.TrashList
	h.ok("trash", "ls", "--by-tags", "draft math", "--json").json(t, &list)
	if len(list.Entries) != 1 || list.Entries[0].OriginalPath != "docs/math.pdf" {
		t.Fatalf("trash ls --by-tags = %+v", list.Entries)
	}
	h.ok("trash", "ls", "other/", "--by-tag", "q1", "--json").json(t, &list)
	if len(list.Entries) != 1 || list.Entries[0].OriginalPath != "other/q1.txt" {
		t.Fatalf("trash ls other/ --by-tag = %+v", list.Entries)
	}
}

func TestTrashPurgeByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	h.ok("rm", "--by-tag", "math", "--by-tag", "q1", "-y")
	count := func() int {
		var list api.TrashList
		h.ok("trash", "ls", "--json").json(t, &list)
		return len(list.Entries)
	}

	if r := h.ok("trash", "purge", "--by-tag", "math", "--dry-run"); len(strings.Split(strings.TrimSpace(r.stdout), "\n")) != 2 || count() != 4 {
		t.Fatalf("dry run = %q", r.stdout)
	}
	if r := h.exec("", "trash", "purge", "--by-tag", "math"); r.code != exitUsage || count() != 4 {
		t.Fatalf("no terminal: exit %d", r.code)
	}
	h.ok("trash", "purge", "--by-tag", "math", "docs/sub/", "-y")
	if count() != 3 {
		t.Fatalf("scoped purge left %d entries", count())
	}
	if r := h.exec("", "trash", "purge", "--by-tag", "q1", "--older-than", "7d", "-y"); r.code != exitNotFound || count() != 3 {
		t.Fatalf("--older-than: exit %d, %d entries", r.code, count())
	}
	h.ok("trash", "purge", "--by-tag", "q1", "-y")
	if count() != 1 {
		t.Fatalf("purge left %d entries", count())
	}
}

func TestTrashRestoreByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	exists := func(p string) bool { return h.exec("", "stat", p).code == 0 }
	h.ok("rm", "--by-tag", "q1", "-y")
	// Two entries for a.txt: only the newer one, tagged q1, is restored.
	h.seedTagged("a.txt", "q1", "new")
	h.ok("rm", "a.txt")

	r := h.ok("trash", "restore", "--by-tag", "q1", "--dry-run")
	if lines := strings.Split(strings.TrimSpace(r.stdout), "\n"); len(lines) != 2 {
		t.Fatalf("dry run = %q", r.stdout)
	}
	h.seedTagged("other/q1.txt")
	r = h.exec("", "trash", "restore", "--by-tag", "q1", "-y")
	if r.code != exitConflict || !strings.Contains(r.stderr, "other/q1.txt") || exists("a.txt") {
		t.Fatalf("conflict: exit %d, stderr %q", r.code, r.stderr)
	}
	h.ok("trash", "restore", "--by-tag", "q1", "-y", "--force")
	var o api.Object
	h.ok("stat", "a.txt", "--json").json(t, &o)
	if strings.Join(o.Tags, ",") != "q1,new" {
		t.Errorf("restored a.txt has tags %v; want the newest entry", o.Tags)
	}
	if r := h.exec("", "trash", "restore", "--by-tag", "q1", "--to", "x"); r.code != exitUsage {
		t.Errorf("--to with --by-tag: exit %d", r.code)
	}
}

func TestLnByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	exists := func(p string) bool { return h.exec("", "stat", p).code == 0 }

	r := h.ok("ln", "--by-tag", "math", "docs/", "links/", "--save-structure", "--dry-run")
	if !strings.Contains(r.stdout, "links/sub/math2.pdf -> docs/sub/math2.pdf") || exists("links/math.pdf") {
		t.Fatalf("dry run = %+v", r)
	}

	// Without a terminal, -y is required.
	if r := h.exec("", "ln", "--by-tag", "math", "links/"); r.code != exitUsage || exists("links/math.pdf") {
		t.Fatalf("no terminal: exit %d", r.code)
	}

	// a.txt and other/q1.txt are fine, x/q1.txt collides with other/q1.txt
	// and dest/a.txt exists: nothing is linked, every conflict is reported.
	h.seedTagged("dest/a.txt")
	h.seedTagged("x/q1.txt", "q1")
	r = h.exec("", "ln", "--by-tag", "q1", "dest/")
	if r.code != exitConflict || !strings.Contains(r.stderr, "a.txt -> dest/a.txt: the destination exists") ||
		!strings.Contains(r.stderr, "would both link to dest/q1.txt") || !strings.Contains(r.stderr, "--skip") || exists("dest/q1.txt") {
		t.Fatalf("conflicts: exit %d, stderr %q", r.code, r.stderr)
	}

	// --skip links the rest without a word; --skip-explicitly reports. The new
	// symlinks copy the tag q1, so the second run selects them too.
	r = h.ok("ln", "--by-tag", "q1", "dest/", "--skip", "-y")
	if strings.Contains(r.stderr, "skipped") || !strings.Contains(r.stdout, "dest/q1.txt -> other/q1.txt") {
		t.Fatalf("--skip = %+v", r)
	}
	r = h.ok("ln", "--by-tag", "q1", "dest2/", "--skip-explicitly", "--json", "-y")
	var objs []api.Object
	r.json(t, &objs)
	if len(objs) != 2 || !strings.Contains(r.stderr, "and x/q1.txt would both link to dest2/q1.txt") {
		t.Fatalf("--skip-explicitly = %+v (%+v)", r, objs)
	}

	// --skip-symlinks ignores the symlinks created above: no conflicts left.
	r = h.ok("ln", "--by-tag", "q1", "dest3/", "--save-structure", "--skip-symlinks", "-y")
	if !strings.Contains(r.stdout, "dest3/x/q1.txt -> x/q1.txt") || exists("dest3/dest/q1.txt") {
		t.Fatalf("--skip-symlinks = %+v", r)
	}

	for _, args := range [][]string{
		{"ln", "--skip", "a.txt", "b.txt"},
		{"ln", "--skip-symlinks", "a.txt", "b.txt"},
		{"ln", "-y", "a.txt", "b.txt"},
		{"ln", "--by-tag", "q1", "-r", "x/"},
		{"ln", "--by-tag", "q1", "--skip", "--skip-explicitly", "y/"},
	} {
		if r := h.exec("", args...); r.code != exitUsage {
			t.Errorf("%v: exit %d", args, r.code)
		}
	}
}

func TestLnByTagSync(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	exists := func(p string) bool { return h.exec("", "stat", p).code == 0 }

	h.ok("ln", "--by-tag", "math", "docs/", "L/", "--save-structure", "-y")
	h.seedTagged("docs/new.pdf", "math")
	r := h.ok("ln", "--by-tag", "math", "docs/", "L/", "--save-structure", "--sync", "-y")
	if got := strings.TrimSpace(r.stdout); !strings.HasPrefix(got, "L/new.pdf -> docs/new.pdf") || strings.Contains(got, "math2") ||
		!strings.Contains(r.stderr, "2 symlink(s) up to date") {
		t.Fatalf("sync = %+v", r)
	}

	// A moved file keeps its symlink instead of getting a second one.
	h.ok("mv", "docs/sub/math2.pdf", "docs/moved.pdf")
	r = h.ok("ln", "--by-tag", "math", "docs/", "L/", "--save-structure", "--sync", "-y")
	if !strings.Contains(r.stderr, "docs/moved.pdf is already linked at L/sub/math2.pdf") || exists("L/moved.pdf") {
		t.Fatalf("moved = %+v", r)
	}

	// A foreign file in the way is still a conflict.
	h.seedTagged("L/x.pdf")
	h.seedTagged("docs/x.pdf", "math")
	if r := h.exec("", "ln", "--by-tag", "math", "docs/", "L/", "--save-structure", "--sync", "-y"); r.code != exitConflict ||
		!strings.Contains(r.stderr, "docs/x.pdf -> L/x.pdf: the destination exists") {
		t.Fatalf("conflict: exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestLnByTagPrune(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	exists := func(p string) bool { return h.exec("", "stat", p).code == 0 }

	h.ok("ln", "--by-tag", "math", "docs/", "L/", "-y")
	h.seedTagged("outside.pdf")
	h.ok("ln", "outside.pdf", "L/manual.pdf")
	h.seedTagged("docs/new.pdf", "math")
	h.ok("meta", "docs/math.pdf", "--untag", "math")
	h.ok("rm", "docs/sub/math2.pdf")

	// Without --prune-trashed, the symlink to the trashed file is kept.
	r := h.ok("ln", "--by-tag", "math", "docs/", "L/", "--sync", "--prune", "--dry-run")
	if got := strings.TrimSpace(r.stdout); got != "+ L/new.pdf -> docs/new.pdf\n- L/math.pdf (no longer selected)" {
		t.Fatalf("plan without --prune-trashed = %q", got)
	}
	r = h.ok("ln", "--by-tag", "math", "docs/", "L/", "--sync", "--prune", "--prune-trashed", "--dry-run")
	want := "+ L/new.pdf -> docs/new.pdf\n- L/math.pdf (no longer selected)\n- L/math2.pdf (the file is in the trash)"
	if got := strings.TrimSpace(r.stdout); got != want {
		t.Fatalf("plan = %q", got)
	}
	var res struct {
		Created []api.Object `json:"created"`
		Pruned  []string     `json:"pruned"`
	}
	h.ok("ln", "--by-tag", "math", "docs/", "L/", "--sync", "--prune", "--prune-trashed", "-y", "--json").json(t, &res)
	if len(res.Created) != 1 || len(res.Pruned) != 2 || exists("L/math.pdf") || !exists("L/manual.pdf") || !exists("L/new.pdf") {
		t.Fatalf("prune = %+v", res)
	}
	if r := h.exec("", "ln", "--by-tag", "math", "L/", "--prune", "-y"); r.code != exitUsage {
		t.Errorf("--prune without --sync: exit %d", r.code)
	}
	if r := h.exec("", "ln", "--by-tag", "math", "L/", "--sync", "--prune-trashed", "-y"); r.code != exitUsage {
		t.Errorf("--prune-trashed without --prune: exit %d", r.code)
	}
}

func TestLnByTagPruneFreesPlace(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)

	// L/math.pdf links docs/math.pdf, which loses its tag; docs/sub/math.pdf
	// gains it and wants the same flat place.
	h.ok("ln", "--by-tag", "math", "docs/", "L/", "-y")
	h.ok("meta", "docs/math.pdf", "--untag", "math")
	h.seedTagged("docs/sub/math.pdf", "math")

	r := h.exec("", "ln", "--by-tag", "math", "docs/", "L/", "--sync", "--prune", "--strict", "-y")
	if r.code != exitConflict || !strings.Contains(r.stderr, "docs/sub/math.pdf -> L/math.pdf: the destination exists") {
		t.Fatalf("--strict: exit %d, stderr %q", r.code, r.stderr)
	}
	r = h.ok("ln", "--by-tag", "math", "docs/", "L/", "--sync", "--prune", "-y")
	if !strings.Contains(r.stderr, "docs/sub/math.pdf takes the place of the pruned symlink L/math.pdf") ||
		!strings.Contains(r.stdout, "L/math.pdf -> docs/sub/math.pdf") {
		t.Fatalf("replace = %+v", r)
	}
	if r := h.exec("", "ln", "--by-tag", "math", "L/", "--sync", "--strict", "-y"); r.code != exitUsage {
		t.Errorf("--strict without --prune: exit %d", r.code)
	}
}

func TestLnByTagPruneMovedOut(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	seedTags(h)
	exists := func(p string) bool { return h.exec("", "stat", p).code == 0 }

	h.ok("ln", "--by-tag", "math", "docs/", "L/", "-y")
	h.seedTagged("own.pdf")
	h.ok("ln", "own.pdf", "L/own.pdf")
	h.ok("mv", "docs/math.pdf", "elsewhere/math.pdf")

	r := h.ok("ln", "--by-tag", "math", "docs/", "L/", "--sync", "--prune", "-y")
	if !strings.Contains(r.stdout, "Moved L/math.pdf to the trash (the file moved out of scope)") ||
		exists("L/math.pdf") || !exists("L/own.pdf") || !exists("L/math2.pdf") {
		t.Fatalf("moved out = %+v", r)
	}
}
