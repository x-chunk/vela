package client_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/testutil"
)

func TestTrashAndLinks(t *testing.T) {
	t.Parallel()
	ts := testutil.NewServer(t)
	c := newClient(t, ts)
	ctx := context.Background()
	if _, _, err := c.Upload(ctx, "docs/report q1.txt", strings.NewReader("hello"), client.UploadOptions{Size: 5, Visibility: api.Private}); err != nil {
		t.Fatal(err)
	}

	link, err := c.CreateLink(ctx, api.CreateLinkRequest{Path: "/docs/report q1.txt", TTL: "1h", Note: "n"})
	if err != nil || link.URL == nil {
		t.Fatalf("CreateLink = %+v, %v", link, err)
	}
	res, err := http.Get(*link.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("link status = %d", res.StatusCode)
	}
	links, err := c.ListLinks(ctx, client.LinkListOptions{Path: "docs/report q1.txt"})
	if err != nil || len(links.Links) != 1 {
		t.Fatalf("ListLinks = %+v, %v", links, err)
	}
	if err := c.RevokeLink(ctx, link.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Delete(ctx, "docs/", client.DeleteOptions{Recursive: true}); err != nil {
		t.Fatal(err)
	}
	trash, err := c.ListAllTrash(ctx, client.TrashOptions{Path: "docs/report q1.txt"})
	if err != nil || len(trash) != 1 {
		t.Fatalf("ListAllTrash = %+v, %v", trash, err)
	}
	if _, err := c.Restore(ctx, trash[0].ID, api.RestoreRequest{Path: "restored.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Delete(ctx, "restored.txt", client.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	trash, _ = c.ListAllTrash(ctx, client.TrashOptions{})
	if err := c.PurgeTrashEntry(ctx, trash[0].ID); err != nil {
		t.Fatal(err)
	}
	if pr, err := c.PurgeTrash(ctx, "1d"); err != nil || pr.Purged != 0 {
		t.Fatalf("PurgeTrash = %+v, %v", pr, err)
	}
}
