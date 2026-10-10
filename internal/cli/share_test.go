package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

func TestShareAndLinks(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("private/report.pdf", "%PDF", api.Private)
	var copied string
	h.run = func(_ context.Context, stdin io.Reader, _ bool, name string, _ ...string) error {
		b, _ := io.ReadAll(stdin)
		copied = name + ":" + string(b)
		return nil
	}
	var l api.Link
	h.ok("share", "private/report.pdf", "--ttl", "2h", "--note", "teacher", "--copy", "--json").json(t, &l)
	if l.URL == nil || l.Note != "teacher" || !strings.HasSuffix(copied, *l.URL) {
		t.Fatalf("share = %+v (copied %q)", l, copied)
	}
	r := h.ok("links", "ls")
	if !strings.Contains(r.stdout, l.ID) || !strings.Contains(r.stdout, "teacher") {
		t.Fatalf("links ls = %s", r.stdout)
	}
	h.ok("links", "revoke", l.ID)
	var list api.LinkList
	h.ok("links", "ls", "--json").json(t, &list)
	if len(list.Links) != 0 {
		t.Fatalf("revoked link still listed: %+v", list)
	}
	h.ok("links", "ls", "--all", "--json").json(t, &list)
	if len(list.Links) != 1 || list.Links[0].RevokedAt == nil {
		t.Fatalf("links ls --all = %+v", list)
	}
	if r := h.exec("", "share", "private/report.pdf", "--ttl", "99d"); r.code != exitError {
		t.Fatalf("over-long TTL = %+v", r)
	}
}
