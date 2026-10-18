package index

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
)

var now = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func TestRenderIndex(t *testing.T) {
	t.Parallel()
	th, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	page := BuildPage(Site{Title: "store.example.com", URL: "https://store.example.com"}, "1.0.0", "docs/math/",
		[]DirEntry{{Path: "docs/math/q10/", Count: 3, UpdatedAt: now}, {Path: "docs/math/q2/", Count: 1, UpdatedAt: now}},
		[]domain.Object{
			{Path: "docs/math/test 2.pdf", Size: 482193, ContentType: "application/pdf", Title: "Контрольная <b>", Tags: []string{"q1"}, UpdatedAt: now},
			{Path: "docs/math/a.png", Size: 10, ContentType: "image/png", UpdatedAt: now},
		}, now, nil)
	var buf bytes.Buffer
	if err := th.RenderIndex(&buf, page); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{
		`<a href="/docs/">docs</a>`,
		`<a href="/docs/math/">math</a>`,
		`href="/docs/math/test%202.pdf"`,
		`Контрольная &lt;b&gt;`, // escaped by html/template
		`<span class="tag">q1</span>`,
		`470.9 KiB`,
		`3 files`,
		`1 file<`,
		`href="/docs/"`, // parent link
		`/_vela/assets/style.css?v=`,
		`kind-pdf`,
		`<title>/docs/math/ · store.example.com</title>`,
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

func TestRenderSessionPage(t *testing.T) {
	t.Parallel()
	th, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := th.RenderIndex(&buf, sampleSessionPage()); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{
		`<span class="link"><a href="/a/b/">`,
		`<span class="flag flag-hidden" role="img" tabindex="0" aria-label="Hidden: not shown to anonymous visitors in the parent listing" data-tip="Hidden: not shown to anonymous visitors in the parent listing"><svg class="icon icon-hidden"`,
		`data-tip="Index disabled for anonymous visitors"><svg class="icon icon-no-index"`,
		`data-tip="Private: needs a key or signed link"><svg class="icon icon-lock"`,
		`data-tip="Unlisted: anyone with the URL can open it"><svg class="icon icon-unlisted"`,
		`<span class="session">signed in: /a/</span>`,
		`<form class="signout" method="post" action="/_vela/logout"><button type="submit">sign out</button></form>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("signed-in page lacks %q", want)
		}
	}
	if strings.Contains(html, "style=") || strings.Contains(html, "<script") || strings.Contains(html, `title="Private`) {
		t.Error("indicators use inline styles, scripts or native tooltips")
	}
	if strings.Contains(html, "sign in<") {
		t.Error("signed-in page offers to sign in")
	}
	// Indicators sit right after the name, outside the link.
	if !strings.Contains(html, `<span class="name">b/</span></a><span class="flag flag-hidden"`) {
		t.Error("directory flags do not follow the name")
	}

	full := sampleSessionPage()
	full.Session = &Session{Full: true, SignOutURL: "/_vela/logout"}
	buf.Reset()
	if err := th.RenderIndex(&buf, full); err != nil || !strings.Contains(buf.String(), "signed in: full access") {
		t.Fatalf("full access footer missing: %v", err)
	}

	anonymous := samplePage()
	anonymous.SignInURL = "/_vela/login?next=%2Fa%2F"
	buf.Reset()
	if err := th.RenderIndex(&buf, anonymous); err != nil {
		t.Fatal(err)
	}
	if html := buf.String(); !strings.Contains(html, `<a class="auth" href="/_vela/login?next=%2Fa%2F">sign in</a>`) ||
		strings.Contains(html, "<form") || strings.Contains(html, `class="flag`) {
		t.Fatalf("anonymous footer = %s", html)
	}
}

func TestFlags(t *testing.T) {
	t.Parallel()
	page := sampleSessionPage()
	if got := page.Dirs[0].Flags; len(got) != 2 || got[0] != FlagHidden || got[1] != FlagNoIndex {
		t.Fatalf("dir flags = %v", got)
	}
	for _, f := range page.Files {
		if len(f.Flags) != 1 {
			t.Fatalf("file %s flags = %v", f.Name, f.Flags)
		}
	}
	public := BuildPage(Site{}, "", "", nil, []domain.Object{{Path: "p", Visibility: api.Public}}, now, &Session{Full: true})
	if public.Files[0].Flags != nil {
		t.Fatalf("public file flagged: %v", public.Files[0].Flags)
	}
	// Anonymous pages never carry flags or a session, so their JSON keeps
	// its previous shape.
	anon := BuildPage(Site{}, "", "", []DirEntry{{Path: "d/", Hidden: true}}, []domain.Object{{Path: "p", Visibility: api.Private}}, now, nil)
	b, err := json.Marshal(anon)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "flags") || strings.Contains(s, "session") || strings.Contains(s, "sign") {
		t.Fatalf("anonymous JSON = %s", s)
	}
	b, err = json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, `"session":{"prefix":"a/","full":false}`) || !strings.Contains(s, `"flags":["hidden","no-index"]`) {
		t.Fatalf("signed-in JSON = %s", s)
	}
	if flagSVG("unknown") != "" {
		t.Fatal("unknown flag rendered")
	}
	for name := range flags {
		if flags[name].icon == "" || iconPaths[flags[name].icon] == "" {
			t.Errorf("flag %s has no built-in icon", name)
		}
	}
}

func TestRenderLogin(t *testing.T) {
	t.Parallel()
	th, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = th.RenderLogin(&buf, LoginPage{Site: Site{Title: "s"}, Title: "Sign in", Action: "/_vela/login?next=%2Fdocs%2F", Error: "Invalid API key.", NoIndex: true, GeneratedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{
		`<title>Sign in · s</title>`,
		`<meta name="robots" content="noindex, nofollow">`,
		`<form method="post" action="/_vela/login?next=%2Fdocs%2F">`,
		`<input type="password" name="key" autocomplete="off"`,
		`<p class="message" role="alert">Invalid API key.</p>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("login page lacks %q", want)
		}
	}
	if strings.Contains(html, "style=") || strings.Contains(html, "<script") || strings.Count(html, "<input") != 1 || strings.Count(html, "<button") != 1 {
		t.Errorf("login page is not one field and one button without inline code: %s", html)
	}
}

func TestOldThemeWithoutSignIn(t *testing.T) {
	t.Parallel()
	// A custom layout written before web sign-in, using only fields that
	// Page and ErrorPage share, still loads, and the embedded login page
	// renders inside it.
	dir := t.TempDir()
	layout := `{{define "layout"}}<html{{if .NoIndex}} data-noindex{{end}}{{if .RootDisabled}} data-root-off{{end}}><title>{{.Title}} · {{.Site.Title}}</title>` +
		`{{template "content" .}}<footer>{{.Version}} {{date .GeneratedAt}}</footer></html>{{end}}`
	if err := os.WriteFile(filepath.Join(dir, "layout.html"), []byte(layout), 0o644); err != nil {
		t.Fatal(err)
	}
	th, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := th.RenderLogin(&buf, LoginPage{Site: Site{Title: "s"}, Title: "Sign in", Action: "/_vela/login"}); err != nil ||
		!strings.Contains(buf.String(), `name="key"`) || !strings.Contains(buf.String(), "<title>Sign in · s</title>") {
		t.Fatalf("login with old layout = %s, %v", buf.String(), err)
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "login.html"), []byte(`{{define "content"}}{{.Missing}}{{end}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(broken); err == nil {
		t.Fatal("broken login.html accepted")
	}
}
