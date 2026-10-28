package client_test

import (
	"context"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/testutil"
)

// TestTagFilterOnOlderServers checks that tag filters hold even against a
// server that ignores the tag parameter: callers delete what List returns.
func TestTagFilterOnOlderServers(t *testing.T) {
	t.Parallel()
	ts := testutil.NewServer(t)
	ctx := context.Background()
	backend, _ := url.Parse(ts.URL)
	var sawTag bool
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(backend)
		r.Out.Host = r.In.Host
		q := r.Out.URL.Query()
		sawTag = sawTag || q.Has("tag")
		q.Del("tag")
		r.Out.URL.RawQuery = q.Encode()
	}}
	old := httptest.NewServer(proxy)
	t.Cleanup(old.Close)

	c, err := client.New(old.URL, ts.AdminKey)
	if err != nil {
		t.Fatal(err)
	}
	tags := []string{"math"}
	for _, p := range []string{"a.txt", "b.txt"} {
		opts := client.UploadOptions{Size: 1}
		if p == "a.txt" {
			opts.Tags = tags
		}
		if _, _, err := c.Upload(ctx, p, strings.NewReader("x"), opts); err != nil {
			t.Fatal(err)
		}
	}
	filter := api.TagFilter{{"math"}}
	l, err := c.ListAll(ctx, client.ListOptions{Recursive: true, Tags: filter})
	if err != nil || len(l.Objects) != 1 || l.Objects[0].Path != "a.txt" {
		t.Fatalf("ListAll = %+v, %v", l, err)
	}
	for _, p := range []string{"a.txt", "b.txt"} {
		if _, err := c.Delete(ctx, p, client.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := c.ListAllTrash(ctx, client.TrashOptions{Tags: filter})
	if err != nil || len(entries) != 1 || entries[0].OriginalPath != "a.txt" {
		t.Fatalf("ListAllTrash = %+v, %v", entries, err)
	}
	if !sawTag {
		t.Error("the tag parameter was never sent")
	}
}
