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
