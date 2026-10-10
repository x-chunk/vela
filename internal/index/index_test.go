package index

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/internal/domain"
)

var now = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func TestRenderIndex(t *testing.T) {
	t.Parallel()
	th, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	page := BuildIndexPage(Site{Title: "store.xchunk.org", URL: "https://store.xchunk.org"}, "1.0.0", "edu/math/",
		[]DirEntry{{Path: "edu/math/q10/", Count: 3, UpdatedAt: now}, {Path: "edu/math/q2/", Count: 1, UpdatedAt: now}},
		[]domain.Object{
			{Path: "edu/math/test 2.pdf", Size: 482193, ContentType: "application/pdf", Title: "Контрольная <b>", Tags: []string{"q1"}, UpdatedAt: now},
			{Path: "edu/math/a.png", Size: 10, ContentType: "image/png", UpdatedAt: now},
		}, now)
	var buf bytes.Buffer
	if err := th.RenderIndex(&buf, page); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{
		`<a href="/edu/">edu</a>`,
		`<a href="/edu/math/">math</a>`,
		`href="/edu/math/test%202.pdf"`,
		`Контрольная &lt;b&gt;`, // escaped by html/template
		`<span class="tag">q1</span>`,
		`470.9 KiB`,
		`3 files`,
		`1 file<`,
		`href="/edu/"`, // parent link
		`/_vela/assets/style.css?v=`,
		`kind-pdf`,
		`<title>/edu/math/ · store.xchunk.org</title>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index page lacks %q", want)
		}
	}
	if strings.Contains(html, "style=") || strings.Contains(html, "<script") {
		t.Error("page uses inline styles or scripts, which the CSP forbids")
	}
	// Natural order: q2 before q10.
	if strings.Index(html, "q2/") > strings.Index(html, "q10/") {
		t.Error("directories are not in natural order")
	}
}

func TestRenderError(t *testing.T) {
	t.Parallel()
	th, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = th.RenderError(&buf, ErrorPage{Site: Site{Title: "s"}, Status: 410, Title: "Link expired", Message: "Ask for a new link.", GeneratedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "410") || !strings.Contains(buf.String(), "Ask for a new link.") {
		t.Fatalf("error page = %s", buf.String())
	}
}

func TestAssets(t *testing.T) {
	t.Parallel()
	th, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"style.css", "favicon.svg", "favicon.ico"} {
		a, ok := th.Asset(name)
		if !ok || len(a.Content) == 0 || len(a.Hash) != 12 {
			t.Errorf("asset %s = %+v, %v", name, a, ok)
		}
	}
	if _, ok := th.Asset("../layout.html"); ok {
		t.Error("asset lookup escaped the assets directory")
	}
}

func TestThemeOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", `{{define "content"}}CUSTOM {{len .Files}}{{end}}`)
	write("assets/style.css", `body{color:red}`)
	write("assets/extra.css", `x{}`)
	th, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := th.RenderIndex(&buf, samplePage()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "CUSTOM 1") || !strings.Contains(buf.String(), "<footer") {
		t.Fatalf("override not applied or layout lost: %s", buf.String())
	}
	if a, _ := th.Asset("style.css"); string(a.Content) != "body{color:red}" {
		t.Error("asset override not applied")
	}
	if _, ok := th.Asset("extra.css"); !ok {
		t.Error("additional asset not loaded")
	}
	if _, ok := th.Asset("favicon.ico"); !ok {
		t.Error("embedded asset lost when overriding others")
	}

	for name, content := range map[string]string{
		"index.html":  `{{define "content"}}{{.NoSuchField}}{{end}}`,
		"error.html":  `{{define "content"}}{{end`,
		"layout.html": `{{define "layout"}}{{template "content" .}}{{asset "missing.css"}}{{end}}`,
	} {
		bad := t.TempDir()
		if err := os.WriteFile(filepath.Join(bad, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(bad); err == nil {
			t.Errorf("broken %s accepted", name)
		}
	}
	if _, err := Load(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing theme directory accepted")
	}
}

func TestNaturalLess(t *testing.T) {
	t.Parallel()
	in := []string{"lesson10", "Lesson2", "lesson1", "b", "A", "a", "x007", "x7", "10", "9", "Ёж", "ёлка"}
	sort.SliceStable(in, func(i, j int) bool { return NaturalLess(in[i], in[j]) })
	want := "9 10 A a b lesson1 Lesson2 lesson10 x007 x7 Ёж ёлка"
	if got := strings.Join(in, " "); got != want {
		t.Fatalf("natural order:\n got %s\nwant %s", got, want)
	}
}

func TestKindOf(t *testing.T) {
	t.Parallel()
	tests := map[[2]string]string{
		{"a.pdf", "application/pdf"}:             "pdf",
		{"a.png", "image/png"}:                   "image",
		{"a.zip", "application/zip"}:             "archive",
		{"a.tar.gz", "application/gzip"}:         "archive",
		{"a.go", "text/x-go; charset=utf-8"}:     "code",
		{"a.md", "text/markdown; charset=utf-8"}: "text",
		{"a.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}: "spreadsheet",
		{"a.bin", "application/octet-stream"}:                                           "file",
	}
	for in, want := range tests {
		if got := KindOf(in[0], in[1]); got != want {
			t.Errorf("KindOf(%q, %q) = %q; want %q", in[0], in[1], got, want)
		}
	}
}
