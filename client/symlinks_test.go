package client_test

import (
	"context"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/testutil"
)

func TestSymlinks(t *testing.T) {
	t.Parallel()
	ts := testutil.NewServer(t)
	c := newClient(t, ts)
	ctx := context.Background()
	if info, err := c.Info(ctx); err != nil || !info.Features.Symlinks {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	a, _, err := c.Upload(ctx, "orig/a.txt", strings.NewReader("a"), client.UploadOptions{Size: 1, Visibility: api.Public})
	if err != nil {
		t.Fatal(err)
	}

	l, created, err := c.CreateSymlink(ctx, api.CreateSymlinkRequest{Source: "/orig/a.txt", Path: "/ln/a.txt"})
	if err != nil || !created || l.Kind != api.KindSymlink || l.Target.ID != a.ID {
		t.Fatalf("CreateSymlink = %+v, %v, %v", l, created, err)
	}
	r, created, err := c.CreateSymlink(ctx, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "ln/a.txt", Overwrite: true})
	if err != nil || created || r.ID != l.ID {
		t.Fatalf("retarget = %+v, %v, %v", r, created, err)
	}
	_, _, err = c.CreateSymlink(ctx, api.CreateSymlinkRequest{Source: "orig/a.txt", Path: "ln/a.txt"})
	if !api.IsCode(err, api.CodeAlreadyExists) {
		t.Errorf("duplicate = %v", err)
	}
	tree, err := c.CreateSymlinkTree(ctx, api.CreateSymlinkRequest{Source: "orig/", Path: "tree/"})
	if err != nil || tree.Created != 1 {
		t.Fatalf("CreateSymlinkTree = %+v, %v", tree, err)
	}

	for name, opts := range map[string]client.ListOptions{
		"kind":   {Recursive: true, Kind: api.KindSymlink},
		"target": {Recursive: true, Target: "/orig/a.txt"},
	} {
		l, err := c.List(ctx, opts)
		if err != nil || len(l.Objects) != 2 {
			t.Errorf("%s: %+v, %v", name, l, err)
		}
	}
	if _, err := c.Delete(ctx, "orig/a.txt", client.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	l2, err := c.List(ctx, client.ListOptions{Recursive: true, Broken: true})
	if err != nil || len(l2.Objects) != 2 {
		t.Errorf("broken: %+v, %v", l2, err)
	}
	_, _, err = c.Upload(ctx, "ln/a.txt", strings.NewReader("x"), client.UploadOptions{Size: 1})
	if !api.IsCode(err, api.CodeIsSymlink) {
		t.Errorf("upload onto symlink = %v", err)
	}
}
