package server_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/testutil"
)

// These fixtures were captured before directory policies were implemented.
func TestIndexWithoutPoliciesGolden(t *testing.T) {
	h := newHarness(t, testutil.WithStartTime(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)))
	for _, p := range []string{"top.txt", "docs/a.txt", "docs/sub/b.txt", "docs/sub/c.txt"} {
		h.upload(p, "example", api.HeaderVisibility, "public")
	}
	h.upload("docs/private.txt", "secret", api.HeaderVisibility, "private")
	for _, tc := range []struct{ name, route string }{{"root.html", "/"}, {"docs.html", "/docs/"}, {"sub.html", "/docs/sub/"}, {"docs.json", "/docs/?format=json"}} {
		r := h.do("GET", tc.route, "", nil)
		expectStatus(t, r, 200)
		body := []byte(strings.ReplaceAll(string(r.body), h.ts.URL, "https://store.example.com"))
		name := filepath.Join("testdata", "index", tc.name)
		want, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(tc.name, ".json") {
			body, want = legacyJSON(t, body), legacyJSON(t, want)
		}
		if !bytes.Equal(body, want) {
			t.Fatalf("index differs from pre-policy fixture %s:\n%s", name, body)
		}
	}
}

func legacyJSON(t *testing.T, b []byte) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	// Additional theme fields are additive to the public JSON representation.
	delete(doc, "title")
	delete(doc, "description")
	delete(doc, "noindex")
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
