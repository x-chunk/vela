package client_test

import (
	"context"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/testutil"
)

func TestDirectoryClient(t *testing.T) {
	t.Parallel()
	ts := testutil.NewServer(t)
	c := newClient(t, ts)
	ctx := context.Background()
	dir, err := c.PutDir(ctx, "/space dir", api.DirPolicyRequest{Index: api.IndexOn, Hidden: true}, client.DirWriteOptions{CreateOnly: true})
	if err != nil || dir.Prefix != "space dir/" || dir.Rule.Revision != 1 || dir.URL != nil {
		t.Fatalf("put: %+v %v", dir, err)
	}
	_, err = c.PutDir(ctx, "space dir/", api.DirPolicyRequest{}, client.DirWriteOptions{CreateOnly: true})
	if !api.IsCode(err, api.CodePreconditionFailed) {
		t.Fatal(err)
	}
	dir, err = c.PatchDir(ctx, "space dir/", api.DirPolicyPatch{Hidden: api.Null[bool](), Title: api.Some("Area")}, client.DirWriteOptions{IfRevision: 1})
	if err != nil || dir.Rule.Hidden || dir.Rule.Title != "Area" || dir.Rule.Revision != 2 {
		t.Fatalf("patch: %+v %v", dir, err)
	}
	if _, err := c.PutDir(ctx, "/", api.DirPolicyRequest{}, client.DirWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PatchDir(ctx, "space dir/sub/", api.DirPolicyPatch{}, client.DirWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	list, err := c.ListAllDirPolicies(ctx, client.DirListOptions{Recursive: true, Limit: 1})
	if err != nil || len(list.Policies) != 3 || list.Policies[0].Prefix != "" {
		t.Fatalf("list: %+v %v", list, err)
	}
	list, err = c.ListAllDirPolicies(ctx, client.DirListOptions{Prefix: "space dir/", Recursive: false, Orphaned: true, Limit: 1})
	if err != nil || len(list.Policies) != 2 {
		t.Fatalf("nonrecursive: %+v %v", list, err)
	}
	if err := c.DeleteDir(ctx, "space dir/", client.DirWriteOptions{IfRevision: 1}); !api.IsCode(err, api.CodePreconditionFailed) {
		t.Fatal(err)
	}
	if err := c.DeleteDir(ctx, "space dir/", client.DirWriteOptions{IfRevision: 2}); err != nil {
		t.Fatal(err)
	}
	dir, err = c.ShowDir(ctx, "space dir/")
	if err != nil || dir.Rule != nil || dir.Effective.IndexSource != "config" {
		t.Fatalf("show: %+v %v", dir, err)
	}
}
