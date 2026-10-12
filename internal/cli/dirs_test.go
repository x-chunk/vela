package cli

import (
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

func TestDirCommands(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("test0/a", "public", api.Public)
	h.seed("test0/sub/b", "public", api.Public)
	r := h.ok("dir", "hide", "test0")
	for _, want := range []string{"Index page:", "not listed in parent", "hidden from search engines"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatal(r.stdout)
		}
	}
	var dir api.EffectiveDir
	h.ok("dir", "show", "test0/", "--json").json(t, &dir)
	if dir.Rule == nil || !dir.Rule.Hidden || dir.Effective.Index != api.IndexOn {
		t.Fatal(dir)
	}
	h.ok("dir", "set", "test0", "--title", "Test", "--description", "Description", "--noindex", "off", "--json").json(t, &dir)
	if dir.Effective.Title != "Test" || dir.Effective.Description != "Description" || dir.Effective.NoIndex != api.IndexOff {
		t.Fatal(dir)
	}
	h.ok("dir", "set", "test0", "--no-hidden", "--index", "inherit")
	r = h.ok("dir", "ls")
	for _, want := range []string{"PREFIX", "EXISTS", "test0/", "(inherit)", "true"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatal(r.stdout)
		}
	}
	h.ok("dir", "hide", "orphan")
	var list api.DirPolicyList
	h.ok("dir", "ls", "--orphaned", "--json").json(t, &list)
	if len(list.Policies) != 1 || list.Policies[0].Prefix != "orphan/" {
		t.Fatal(list)
	}
	h.ok("dir", "rm", "orphan", "--json")
	h.ok("dir", "hide", "test0")
	r = h.ok("ls", "-l")
	if !strings.Contains(r.stdout, "[hidden]") {
		t.Fatal(r.stdout)
	}
	h.ok("dir", "set", "test0", "--index", "off")
	r = h.ok("ls", "-l")
	if !strings.Contains(r.stdout, "[no index]") {
		t.Fatal(r.stdout)
	}
	if r = h.exec("", "url", "test0/"); r.code != exitNotFound || !strings.Contains(r.stderr, "disabled") {
		t.Fatal(r)
	}
	h.ok("dir", "set", "test0", "--index", "on")
	if r = h.ok("url", "test0/"); strings.TrimSpace(r.stdout) != h.ts.URL+"/test0/" {
		t.Fatal(r.stdout)
	}
	r = h.ok("mv", "test0/", "archive/")
	if !strings.Contains(r.stdout, "directory policy") {
		t.Fatal(r.stdout)
	}
	h.ok("dir", "show", "archive/")
	h.ok("dir", "private-root")
	if r = h.exec("", "dir", "rm", "/"); r.code != exitUsage {
		t.Fatal(r)
	}
	h.ok("dir", "rm", "/", "-y")
	for _, args := range [][]string{{"dir", "set", "archive", "--index", "invalid"}, {"dir", "set", "archive", "--hidden", "--no-hidden"}, {"dir", "hide", "/"}, {"dir", "set", "archive", "--title", strings.Repeat("x", 257)}} {
		if r = h.exec("", args...); r.code != exitUsage {
			t.Fatal(args, r)
		}
	}
	for _, args := range [][]string{{"dir", "--help"}, {"dir", "hide", "--help"}} {
		if r = h.ok(args...); !strings.Contains(r.stdout, hiddenDirectoryWarning) {
			t.Fatal(r.stdout)
		}
	}
}

func TestDirScopeAndJSON(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("ci/file", "public", api.Public)
	h.login(h.ts.Key(t, "ci", api.ScopeRW, "ci/"))
	h.ok("dir", "hide", "ci", "--json")
	if r := h.exec("", "dir", "hide", "outside", "--json"); r.code != exitAuth || !strings.Contains(r.stdout, `"forbidden"`) {
		t.Fatal(r)
	}
	if r := h.exec("", "dir", "private-root", "--json"); r.code != exitAuth {
		t.Fatal(r)
	}
	var list api.DirPolicyList
	h.ok("dir", "ls", "--json").json(t, &list)
	if len(list.Policies) != 1 || list.Policies[0].Prefix != "ci/" {
		t.Fatal(list)
	}
	h.login(h.ts.AdminKey)
	h.ok("dir", "set", "destination", "--title", "Prepared")
	if r := h.exec("", "mv", "ci/", "destination/", "--json"); r.code != exitConflict {
		t.Fatal(r)
	}
	var moved api.MoveResponse
	h.ok("mv", "ci/", "destination/", "--force", "--json").json(t, &moved)
	if moved.PoliciesMoved != 1 || moved.Moved != 1 {
		t.Fatal(moved)
	}
}
