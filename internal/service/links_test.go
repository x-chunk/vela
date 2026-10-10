package service_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

func TestSignedLinks(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("private/report q1.pdf", "secret", api.Private)
	l, err := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "private/report q1.pdf", TTL: "2h", Note: "for teacher"})
	if err != nil {
		t.Fatal(err)
	}
	if !l.ExpiresAt.Equal(t0.Add(2*time.Hour)) || l.Path != "private/report q1.pdf" || l.Note != "for teacher" {
		t.Fatalf("link = %+v", l)
	}
	if !strings.HasPrefix(*l.URL, "https://store.example.org/s/"+l.ID+"/report%20q1.pdf?exp=") {
		t.Fatalf("url = %s", *l.URL)
	}
	exp, sig := queryParam(*l.URL, "exp"), queryParam(*l.URL, "sig")

	c, err := e.svc.ResolveLink(e.ctx, l.ID, exp, sig)
	if err != nil || e.read(c) != "secret" {
		t.Fatalf("resolve: %v", err)
	}

	expN, _ := strconv.ParseInt(exp, 10, 64)
	other, _ := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "private/report q1.pdf"})
	forged := []struct{ name, id, exp, sig string }{
		{"extended exp", l.ID, strconv.FormatInt(expN+3600, 10), sig},
		{"swapped link id", other.ID, exp, sig},
		{"bad signature", l.ID, exp, "AAAAAAAAAAAAAAAAAAAAAA"},
		{"garbage exp", l.ID, "soon", sig},
		{"garbage id", "../../etc", exp, sig},
	}
	for _, f := range forged {
		_, err := e.svc.ResolveLink(e.ctx, f.id, f.exp, f.sig)
		wantErr(t, err, service.ErrNotFound)
	}

	// Links follow the object across renames.
	if _, err := e.svc.Move(e.ctx, e.admin, api.MoveRequest{From: "private/report q1.pdf", To: "archive/r.pdf"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ResolveLink(e.ctx, l.ID, exp, sig); err != nil {
		t.Fatalf("link after move: %v", err)
	}

	// Expiry: decided from the URL alone.
	e.clock.Advance(2*time.Hour + time.Second)
	_, err = e.svc.ResolveLink(e.ctx, l.ID, exp, sig)
	wantErr(t, err, service.ErrLinkExpired)
	_, err = e.svc.ResolveLink(e.ctx, "01jzzzzzzzzzzzzzzzzzzzzzzz", exp, "x")
	wantErr(t, err, service.ErrLinkExpired)
}

func TestLinkTTLAndRevocation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("f", "x", api.Private)
	_, err := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "f", TTL: "31d"})
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "f", TTL: "1h", ExpiresAt: ptr(t0.Add(time.Hour))})
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "f", ExpiresAt: ptr(t0.Add(-time.Hour))})
	wantErr(t, err, service.ErrInvalidRequest)
	_, err = e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "missing"})
	wantErr(t, err, service.ErrNotFound)
	l, err := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "f", ExpiresAt: ptr(t0.Add(30 * api.Day))})
	if err != nil {
		t.Fatal(err)
	}
	def, _ := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "f"})
	if !def.ExpiresAt.Equal(t0.Add(24 * time.Hour)) {
		t.Fatalf("default TTL expiry = %v", def.ExpiresAt)
	}

	if err := e.svc.RevokeLink(e.ctx, e.admin, l.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.ResolveLink(e.ctx, l.ID, queryParam(*l.URL, "exp"), queryParam(*l.URL, "sig"))
	wantErr(t, err, service.ErrNotFound)

	active, _ := e.svc.ListLinks(e.ctx, e.admin, service.LinkParams{})
	all, _ := e.svc.ListLinks(e.ctx, e.admin, service.LinkParams{IncludeRevoked: true})
	if len(active.Links) != 1 || len(all.Links) != 2 {
		t.Fatalf("active=%d all=%d", len(active.Links), len(all.Links))
	}
	byPath, _ := e.svc.ListLinks(e.ctx, e.admin, service.LinkParams{Path: "f", IncludeRevoked: true})
	if len(byPath.Links) != 2 {
		t.Fatalf("by path = %d", len(byPath.Links))
	}
	_, err = e.svc.ListLinks(e.ctx, e.principal(api.ScopeRO, ""), service.LinkParams{})
	wantErr(t, err, service.ErrForbidden)
}

func TestLinkPrefixScoping(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("edu/a", "a", api.Private)
	e.put("home/b", "b", api.Private)
	rw := e.principal(api.ScopeRW, "edu/")
	_, err := e.svc.CreateLink(e.ctx, rw, api.CreateLinkRequest{Path: "home/b"})
	wantErr(t, err, service.ErrForbidden)
	mine, err := e.svc.CreateLink(e.ctx, rw, api.CreateLinkRequest{Path: "edu/a"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, _ := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "home/b"})
	list, _ := e.svc.ListLinks(e.ctx, rw, service.LinkParams{})
	if len(list.Links) != 1 || list.Links[0].ID != mine.ID {
		t.Fatalf("prefixed list = %+v", list)
	}
	wantErr(t, e.svc.RevokeLink(e.ctx, rw, theirs.ID), service.ErrNotFound)
}

func TestSigningKeyRotation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("f", "x", api.Private)
	before, _ := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "f"})

	res, err := e.svc.RotateSigningKey(e.ctx, false)
	if err != nil || len(res.RetiredKeys) != 1 || res.NewKeyID == "" {
		t.Fatalf("rotate = %+v, %v", res, err)
	}
	// Retired keys keep verifying.
	if _, err := e.svc.ResolveLink(e.ctx, before.ID, queryParam(*before.URL, "exp"), queryParam(*before.URL, "sig")); err != nil {
		t.Fatalf("old link after rotation: %v", err)
	}
	after, _ := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "f"})
	if queryParam(*after.URL, "sig") == "" {
		t.Fatal("no signature on new link")
	}

	res, err = e.svc.RotateSigningKey(e.ctx, true)
	if err != nil || res.RevokedLinks != 2 || len(res.DeletedKeys) != 2 {
		t.Fatalf("revoke-all = %+v, %v", res, err)
	}
	for _, l := range []api.Link{before, after} {
		_, err := e.svc.ResolveLink(e.ctx, l.ID, queryParam(*l.URL, "exp"), queryParam(*l.URL, "sig"))
		wantErr(t, err, service.ErrNotFound)
	}
	list, _ := e.svc.ListLinks(e.ctx, e.admin, service.LinkParams{IncludeRevoked: true})
	for _, l := range list.Links {
		if l.URL != nil || l.RevokedAt == nil {
			t.Fatalf("link after revoke-all = %+v", l)
		}
	}
	fresh, _ := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "f"})
	if _, err := e.svc.ResolveLink(e.ctx, fresh.ID, queryParam(*fresh.URL, "exp"), queryParam(*fresh.URL, "sig")); err != nil {
		t.Fatalf("new link after revoke-all: %v", err)
	}
}

func TestLinksFollowObjectLifecycle(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.put("docs/a.pdf", "v1", api.Private)
	link, err := e.svc.CreateLink(e.ctx, e.admin, api.CreateLinkRequest{Path: "docs/a.pdf", TTL: "2h"})
	if err != nil {
		t.Fatal(err)
	}
	exp, sig := queryParam(*link.URL, "exp"), queryParam(*link.URL, "sig")
	resolve := func() (string, error) {
		c, err := e.svc.ResolveLink(e.ctx, link.ID, exp, sig)
		if err != nil {
			return "", err
		}
		return e.read(c), nil
	}

	// Overwrite keeps the object ID: the link serves the new content.
	e.put("docs/a.pdf", "v2", api.Private)
	if got, err := resolve(); err != nil || got != "v2" {
		t.Fatalf("after overwrite: %q, %v", got, err)
	}
	// Trash disables the link, restore brings it back.
	res, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "docs/a.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("while trashed: %v", err)
	}
	if _, err := e.svc.Restore(e.ctx, e.admin, res.TrashIDs[0], api.RestoreRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(); err != nil {
		t.Fatalf("after restore: %v", err)
	}
	// Purge cascades to the link records.
	if _, err := e.svc.Delete(e.ctx, e.admin, service.DeleteParams{Path: "docs/a.pdf", Purge: true}); err != nil {
		t.Fatal(err)
	}
	all, _ := e.svc.ListLinks(e.ctx, e.admin, service.LinkParams{IncludeExpired: true, IncludeRevoked: true})
	if len(all.Links) != 0 {
		t.Fatalf("purge did not cascade to links: %+v", all.Links)
	}
}
