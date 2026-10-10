package cli

import (
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

func TestTrash(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("archive/home.html", "h", api.Public)
	h.seed("site/a.css", "a", api.Public)
	h.ok("rm", "archive/home.html")
	h.ok("trash", "restore", "archive/home.html")
	h.ok("rm", "-r", "-y", "site/")
	var tl api.TrashList
	h.ok("trash", "ls", "--json").json(t, &tl)
	if len(tl.Entries) != 1 || tl.Entries[0].OriginalPath != "site/a.css" {
		t.Fatalf("trash = %+v", tl)
	}
	if r := h.ok("trash", "ls"); !strings.Contains(r.stdout, "site/a.css") || !strings.Contains(r.stdout, "PURGED AT") {
		t.Fatalf("trash ls = %q", r.stdout)
	}
	h.ok("trash", "restore", strings.ToUpper(tl.Entries[0].ID), "--to", "restored.css")
	if r := h.exec("", "trash", "restore", "never/deleted.txt"); r.code != exitNotFound {
		t.Fatalf("restore of an unknown path = %+v", r)
	}
	h.ok("rm", "restored.css")
	if r := h.exec("", "trash", "purge"); r.code != exitUsage {
		t.Fatalf("unconfirmed purge = %+v", r)
	}
	if r := h.exec("", "trash", "purge", "x", "--older-than", "1d", "-y"); r.code != exitUsage {
		t.Fatalf("ids with --older-than = %+v", r)
	}
	var pr api.PurgeResponse
	h.ok("trash", "purge", "--older-than", "1d", "-y", "--json").json(t, &pr)
	if pr.Purged != 0 {
		t.Fatalf("purge --older-than = %+v", pr)
	}
	h.ok("trash", "purge", "-y")
	h.ok("trash", "ls", "--json").json(t, &tl)
	if len(tl.Entries) != 0 {
		t.Fatalf("trash after purge = %+v", tl)
	}
}
