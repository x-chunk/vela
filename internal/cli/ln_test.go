package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

func TestLn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("orig/a.txt", "a", api.Public)
	h.seed("orig/b.txt", "b", api.Unlisted)
	h.seed("orig/sub/c.txt", "c", api.Private)

	r := h.ok("ln", "orig/a.txt", "sorted/a.txt")
	if !strings.Contains(r.stdout, "sorted/a.txt -> orig/a.txt") || !strings.Contains(r.stderr, "/sorted/a.txt") {
		t.Errorf("ln = %+v", r)
	}
	// DEST/ keeps the source's name; several sources need a directory.
	r = h.ok("ln", "--json", "orig/a.txt", "orig/b.txt", "multi/")
	var objs []api.Object
	r.json(t, &objs)
	if len(objs) != 2 || objs[0].Path != "multi/a.txt" || objs[1].Path != "multi/b.txt" || objs[1].Kind != api.KindSymlink {
		t.Errorf("ln multi = %+v", objs)
	}
	if r := h.exec("", "ln", "orig/a.txt", "orig/b.txt", "nodir"); r.code != exitUsage {
		t.Errorf("several sources without a directory: %+v", r)
	}
	if r := h.exec("", "ln", "orig/", "x/"); r.code != exitUsage || !strings.Contains(r.stderr, "-r") {
		t.Errorf("directory without -r: %+v", r)
	}

	// The effective visibility warning.
	r = h.ok("ln", "--public", "orig/b.txt", "warn.txt")
	if !strings.Contains(r.stderr, "effective visibility is unlisted") {
		t.Errorf("no visibility warning: %+v", r)
	}

	// Existing destinations need -f; -s is accepted.
	if r := h.exec("", "ln", "orig/b.txt", "sorted/a.txt"); r.code != exitConflict {
		t.Errorf("existing destination: %+v", r)
	}
	var o api.Object
	h.ok("ln", "-sf", "--json", "orig/b.txt", "sorted/a.txt").json(t, &o)
	if o.Target == nil || o.Target.Path == nil || *o.Target.Path != "orig/b.txt" {
		t.Errorf("retarget = %+v", o)
	}

	// Metadata flags and --no-copy-meta.
	h.ok("ln", "--json", "--no-copy-meta", "--title", "T", "--tag", "x", "--private", "orig/a.txt", "meta.txt").json(t, &o)
	if o.Title != "T" || len(o.Tags) != 1 || o.Visibility != api.Private || o.URL != nil {
		t.Errorf("metadata = %+v", o)
	}

	// Recursive, with a dry run first.
	r = h.ok("ln", "-r", "--dry-run", "orig/", "tree/")
	if !strings.Contains(r.stdout, "would link tree/sub/c.txt -> orig/sub/c.txt") {
		t.Errorf("dry run = %+v", r)
	}
	if r := h.ok("ls", "-r", "tree/"); strings.TrimSpace(r.stdout) != "" {
		t.Errorf("dry run created something: %q", r.stdout)
	}
	var tree api.SymlinkTreeResponse
	h.ok("ln", "-r", "--json", "orig", "tree").json(t, &tree)
	if tree.Created != 3 {
		t.Errorf("tree = %+v", tree)
	}
	if r := h.exec("", "ln", "-r", "orig/", "tree/"); r.code != exitConflict || !strings.Contains(r.stderr, "-f") {
		t.Errorf("tree conflict: %+v", r)
	}

	// A destination inside the source is not walked, in a dry run either.
	h.ok("ln", "-r", "orig/", "orig/all/")
	r = h.ok("ln", "-r", "-f", "--dry-run", "orig/", "orig/all/")
	if strings.Contains(r.stdout, "orig/all/all/") {
		t.Errorf("nested dry run = %+v", r)
	}

	// -r --skip-symlinks leaves orig/all/ out; --skip-explicitly reports the
	// destinations that already exist and links nothing else.
	r = h.ok("ln", "-r", "--dry-run", "--skip-symlinks", "orig/", "tree/")
	if strings.Contains(r.stdout, "orig/all/") {
		t.Errorf("dry run with --skip-symlinks = %+v", r)
	}
	h.ok("rm", "tree/a.txt")
	r = h.ok("ln", "-r", "--skip-explicitly", "--skip-symlinks", "orig/", "tree/")
	if !strings.Contains(r.stdout, "1 created, 0 replaced") || !strings.Contains(r.stderr, "skipped tree/sub/c.txt (destination conflicts)") ||
		strings.Contains(r.stderr, "orig/all") {
		t.Errorf("skip = %+v", r)
	}
	if r := h.exec("", "ln", "--skip", "orig/a.txt", "x.txt"); !strings.Contains(r.stderr, "also work with -r") {
		t.Errorf("hint = %+v", r)
	}

	// A partial failure exits 7.
	if r := h.exec("", "ln", "orig/a.txt", "missing.txt", "part/"); r.code != exitPartial {
		t.Errorf("partial: %+v", r)
	}
	if r := h.exec("", "ln", "missing.txt", "x.txt"); r.code != exitNotFound {
		t.Errorf("missing source: %+v", r)
	}
}

func TestLnOldServer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == api.RouteInfo {
			_, _ = io.WriteString(w, `{"server_version":"1.0.0","api_version":1,"public_url":"x","key":{},"limits":{}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"not found"}}`)
	}))
	defer old.Close()
	h.env["VELA_URL"] = old.URL
	r := h.exec("", "ln", "a.txt", "b.txt")
	if r.code != exitError || !strings.Contains(r.stderr, "does not support symlinks") {
		t.Errorf("old server = %+v", r)
	}
}

func TestSymlinksInOtherCommands(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("orig/a.txt", "a", api.Public)
	h.seed("orig/b.txt", "b", api.Public)
	h.ok("ln", "orig/a.txt", "s1/a")
	h.ok("ln", "orig/a.txt", "s1/a2")
	h.ok("ln", "orig/b.txt", "s1/b")

	r := h.ok("ls", "-l", "s1/")
	if !strings.Contains(r.stdout, "a -> orig/a.txt") || !strings.Contains(r.stdout, "l ") {
		t.Errorf("ls -l = %q", r.stdout)
	}
	r = h.ok("stat", "orig/a.txt")
	if !strings.Contains(r.stdout, "Symlinks:") || !strings.Contains(r.stdout, "s1/a2") {
		t.Errorf("stat of a target = %q", r.stdout)
	}
	r = h.ok("stat", "s1/a")
	if !strings.Contains(r.stdout, "Target:") || !strings.Contains(r.stdout, "orig/a.txt") {
		t.Errorf("stat of a symlink = %q", r.stdout)
	}
	if r := h.ok("cat", "s1/a"); r.stdout != "a" {
		t.Errorf("cat = %q", r.stdout)
	}
	r = h.exec("", "put", h.file("x.txt", "x"), "s1/a", "--force")
	if r.code != exitConflict || !strings.Contains(r.stderr, "vela ln -f") {
		t.Errorf("put onto symlink = %+v", r)
	}

	// Deleting a target warns and breaks its symlinks.
	r = h.ok("rm", "orig/a.txt")
	if !strings.Contains(r.stderr, "2 symlink(s) now point to a trashed file") {
		t.Errorf("rm warning = %q", r.stderr)
	}
	r = h.ok("ls", "-l", "s1/")
	if !strings.Contains(r.stdout, "(broken: trashed)") {
		t.Errorf("ls -l broken = %q", r.stdout)
	}
	r = h.ok("stat", "s1/a")
	if !strings.Contains(r.stderr, "vela trash restore orig/a.txt") {
		t.Errorf("stat hint = %q", r.stderr)
	}
	var l api.Listing
	h.ok("ls", "--json", "--broken", "s1/").json(t, &l)
	if len(l.Objects) != 2 {
		t.Errorf("ls --broken = %+v", l.Objects)
	}
	h.ok("ls", "--json", "--type", "symlink", "-r").json(t, &l)
	if len(l.Objects) != 3 {
		t.Errorf("ls --type symlink = %+v", l.Objects)
	}
	h.ok("ls", "--json", "-r", "--target", "orig/b.txt").json(t, &l)
	if len(l.Objects) != 1 || l.Objects[0].Path != "s1/b" {
		t.Errorf("ls --target = %+v", l.Objects)
	}

	// get -r skips broken symlinks and exits 7.
	if r := h.exec("", "get", "-r", "s1/", h.dir+"/out"); r.code != exitPartial {
		t.Errorf("get -r = %+v", r)
	}

	// rm --broken needs confirmation without a terminal, then removes only them.
	if r := h.exec("", "rm", "--broken", "s1/"); r.code == 0 {
		t.Errorf("rm --broken without -y = %+v", r)
	}
	h.ok("rm", "--broken", "-y", "s1/")
	h.ok("ls", "--json", "-r", "s1/").json(t, &l)
	if len(l.Objects) != 1 || l.Objects[0].Path != "s1/b" {
		t.Errorf("after rm --broken = %+v", l.Objects)
	}
	r = h.ok("trash", "ls")
	if !strings.Contains(r.stdout, "s1/a -> orig/a.txt") {
		t.Errorf("trash ls = %q", r.stdout)
	}
	h.seed("orig/c.txt", "c", api.Public)
	h.ok("ln", "orig/c.txt", "s1/c")
	h.ok("rm", "orig/c.txt")
	r = h.ok("trash", "purge", "-y")
	if !strings.Contains(r.stderr, "1 symlink(s) now point to a deleted file") {
		t.Errorf("purge warning = %q", r.stderr)
	}
}

func TestRmBrokenIgnoresOldServers(t *testing.T) {
	t.Parallel()
	got := keepObjects([]api.Object{{Path: "file"}, {Path: "link", Kind: api.KindSymlink, Target: &api.SymlinkTarget{State: api.TargetOK}}}, "", true, false)
	if len(got) != 0 {
		t.Errorf("keepObjects = %+v; an old server's files must never count as broken", got)
	}
}
