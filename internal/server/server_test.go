package server_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/server"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/testutil"
)

type harness struct {
	t  *testing.T
	ts *testutil.Server
}

func newHarness(t *testing.T, opts ...testutil.Option) *harness {
	t.Helper()
	return &harness{t: t, ts: testutil.NewServer(t, opts...)}
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (r resp) errCode(t *testing.T) api.ErrorCode {
	t.Helper()
	var e api.ErrorResponse
	r.json(t, &e)
	if e.Error == nil {
		t.Fatalf("no error in %s", r.body)
	}
	return e.Error.Code
}

func (h *harness) do(method, path, key string, body io.Reader, headers ...string) resp {
	h.t.Helper()
	req, err := http.NewRequest(method, h.ts.URL+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, body: b}
}

func (h *harness) admin(method, path, body string, headers ...string) resp {
	h.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	return h.do(method, path, h.ts.AdminKey, r, headers...)
}

func (h *harness) upload(path, content string, headers ...string) api.Object {
	h.t.Helper()
	r := h.admin("PUT", api.ContentRoute(path), content, headers...)
	if r.status != http.StatusCreated && r.status != http.StatusOK {
		h.t.Fatalf("upload %s: %d %s", path, r.status, r.body)
	}
	var o api.Object
	r.json(h.t, &o)
	return o
}

func expectStatus(t *testing.T, r resp, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("status = %d; want %d; body %s", r.status, want, r.body)
	}
}

func TestHealthAndRequestID(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	r := h.do("GET", "/healthz", "", nil)
	expectStatus(t, r, 200)
	if string(r.body) != "{\"status\":\"ok\"}\n" || r.header.Get("X-Request-ID") == "" {
		t.Fatalf("healthz = %s, %v", r.body, r.header)
	}
	// Requests from the trusted proxy (127.0.0.1 in tests) keep their ID.
	r = h.do("GET", "/healthz", "", nil, "X-Request-ID", "edge-123")
	if r.header.Get("X-Request-ID") != "edge-123" {
		t.Fatalf("request ID not propagated: %v", r.header)
	}
	r = h.do("GET", "/healthz", "", nil, "X-Request-ID", "bad id with spaces")
	if r.header.Get("X-Request-ID") == "bad id with spaces" {
		t.Fatal("invalid request ID accepted")
	}
}

func TestAuthentication(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, key := range []string{"", "nonsense", "vela_aaaaaaaaaa_" + strings.Repeat("A", 43)} {
		r := h.do("GET", api.RouteInfo, key, nil)
		expectStatus(t, r, 401)
		if r.errCode(t) != api.CodeUnauthorized {
			t.Fatalf("code = %s", r.body)
		}
	}
	// Unknown endpoints are not revealed to unauthenticated clients.
	expectStatus(t, h.do("GET", "/api/v2/whatever", "", nil), 401)
	// Keys in the query string are ignored.
	expectStatus(t, h.do("GET", api.RouteInfo+"?key="+url.QueryEscape(h.ts.AdminKey), "", nil), 401)

	r := h.admin("GET", api.RouteInfo, "")
	expectStatus(t, r, 200)
	var info api.Info
	r.json(t, &info)
	if info.APIVersion != 1 || info.Key.Name != "admin" || info.Key.Scope != api.ScopeAdmin || info.PublicURL != h.ts.URL {
		t.Fatalf("info = %+v", info)
	}
}

func TestAuthRateLimit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// All test requests come from 127.0.0.1, which is trusted, so X-Real-IP
	// selects the client address.
	for i := 0; i < 10; i++ {
		expectStatus(t, h.do("GET", api.RouteInfo, "bad", nil, "X-Real-IP", "198.51.100.9"), 401)
	}
	r := h.do("GET", api.RouteInfo, h.ts.AdminKey, nil, "X-Real-IP", "198.51.100.9")
	expectStatus(t, r, 429)
	if r.errCode(t) != api.CodeRateLimited || r.header.Get("Retry-After") == "" {
		t.Fatalf("429 without Retry-After: %v %s", r.header, r.body)
	}
	expectStatus(t, h.do("GET", api.RouteInfo, h.ts.AdminKey, nil, "X-Real-IP", "198.51.100.10"), 200)
	h.ts.Clock.Advance(time.Minute)
	expectStatus(t, h.do("GET", api.RouteInfo, h.ts.AdminKey, nil, "X-Real-IP", "198.51.100.9"), 200)
}

func TestAPIRouting(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	r := h.admin("POST", api.RouteInfo, "")
	expectStatus(t, r, 405)
	if r.header.Get("Allow") != "GET, HEAD" || r.errCode(t) != api.CodeMethodNotAllowed {
		t.Fatalf("405 = %v %s", r.header, r.body)
	}
	r = h.admin("GET", "/api/v1/nope", "")
	expectStatus(t, r, 404)
	if r.errCode(t) != api.CodeNotFound {
		t.Fatal(string(r.body))
	}
	r = h.admin("GET", "/api/v1/objects/a//b", "")
	expectStatus(t, r, 400)
	if r.errCode(t) != api.CodeInvalidPath {
		t.Fatal(string(r.body))
	}
	if r.header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
		t.Fatalf("API headers: %v", r.header)
	}
}

func TestCreateAndStat(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	r := h.admin("POST", api.RouteObject, `{"path":"/notes/todo.md","content":"# hi\n","visibility":"public","tags":["A"]}`)
	expectStatus(t, r, 201)
	if r.header.Get("Location") != "/api/v1/objects/notes/todo.md" || r.header.Get("ETag") != `"r1"` {
		t.Fatalf("headers = %v", r.header)
	}
	var o api.Object
	r.json(t, &o)
	if o.ContentType != "text/markdown; charset=utf-8" || o.URL == nil || *o.URL != h.ts.URL+"/notes/todo.md" {
		t.Fatalf("object = %+v", o)
	}
	tests := []struct {
		body string
		code api.ErrorCode
	}{
		{`{"path":"notes/todo.md","content":"x"}`, api.CodeAlreadyExists},
		{`{"path":"notes/todo.md/x","content":"x"}`, api.CodePathConflict},
		{`{"path":"api/x","content":"x"}`, api.CodeInvalidPath},
		{`{"path":"x","content":"x","encoding":"rot13"}`, api.CodeUnsupportedEncoding},
		{`{"path":"x","content":"x","flavor":"red"}`, api.CodeInvalidRequest},
		{`{"path":"x"`, api.CodeInvalidRequest},
		{`{"path":"x","content":"x"} {}`, api.CodeInvalidRequest},
		{fmt.Sprintf(`{"path":"big","content":%q}`, strings.Repeat("a", 65<<10)), api.CodePayloadTooLarge},
	}
	for _, tt := range tests {
		r := h.admin("POST", api.RouteObject, tt.body)
		if r.errCode(t) != tt.code || r.status != tt.code.Status() {
			t.Errorf("POST %.60s: %d %s; want %s", tt.body, r.status, r.body, tt.code)
		}
	}
	r = h.admin("GET", api.ObjectRoute("notes/todo.md"), "")
	expectStatus(t, r, 200)
	if r.header.Get("ETag") != `"r1"` {
		t.Fatalf("stat ETag = %q", r.header.Get("ETag"))
	}
	var e api.ErrorResponse
	r = h.admin("GET", api.ObjectRoute("missing"), "")
	expectStatus(t, r, 404)
	r.json(t, &e)
	if e.Error.RequestID == "" || e.Error.RequestID != r.header.Get("X-Request-ID") {
		t.Fatalf("request_id missing from error: %s", r.body)
	}
}

func TestUpload(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	content := "hello world"
	r := h.admin("PUT", api.ContentRoute("docs/report q1.txt"), content,
		"Content-Type", "application/octet-stream",
		api.HeaderVisibility, "public",
		api.HeaderTitle, api.EncodeHeaderText("Контрольная Q1"),
		api.HeaderTags, "math, q1",
		api.HeaderDisposition, "attachment",
		api.HeaderExpiresAt, time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339),
		api.HeaderSHA256, "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9", // gitleaks:allow -- SHA-256 of "hello world".
		"If-None-Match", "*")
	expectStatus(t, r, 201)
	var o api.Object
	r.json(t, &o)
	if o.Title != "Контрольная Q1" || strings.Join(o.Tags, ",") != "math,q1" || o.Disposition != api.Attachment ||
		o.ExpiresAt == nil || o.ContentType != "text/plain; charset=utf-8" || o.Visibility != api.Public {
		t.Fatalf("uploaded = %+v", o)
	}
	if r.header.Get("Location") != "/api/v1/objects/docs/report%20q1.txt" {
		t.Fatalf("Location = %q", r.header.Get("Location"))
	}

	cases := []struct {
		headers []string
		body    string
		status  int
		code    api.ErrorCode
	}{
		{[]string{"If-None-Match", "*"}, "x", 409, api.CodeAlreadyExists},
		{[]string{"If-Match", `"r9"`}, "x", 412, api.CodePreconditionFailed},
		{[]string{"If-Match", "r1"}, "x", 400, api.CodeInvalidRequest},
		{[]string{api.HeaderSHA256, strings.Repeat("0", 64)}, "x", 400, api.CodeChecksumMismatch},
		{[]string{api.HeaderSHA256, "zz"}, "x", 400, api.CodeInvalidRequest},
		{[]string{api.HeaderVisibility, "secret"}, "x", 400, api.CodeInvalidRequest},
		{[]string{api.HeaderExpiresAt, "tomorrow"}, "x", 400, api.CodeInvalidRequest},
		{[]string{"If-None-Match", `"r1"`}, "x", 400, api.CodeInvalidRequest},
	}
	for _, c := range cases {
		r := h.admin("PUT", api.ContentRoute("docs/report q1.txt"), c.body, c.headers...)
		if r.status != c.status || r.errCode(t) != c.code {
			t.Errorf("PUT with %v: %d %s; want %d %s", c.headers, r.status, r.body, c.status, c.code)
		}
	}

	r = h.admin("PUT", api.ContentRoute("docs/report q1.txt"), "second", "If-Match", `"r1"`, api.HeaderExpiresAt, "none")
	expectStatus(t, r, 200)
	r.json(t, &o)
	if o.Revision != 2 || o.Title != "Контрольная Q1" || o.ExpiresAt != nil {
		t.Fatalf("overwrite = %+v", o)
	}

}

func TestUploadLimits(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testutil.WithServiceOptions(func(o *service.Options) { o.MaxUpload = 1024 }))
	// Declared size over the limit: rejected before the body is read.
	r := h.admin("PUT", api.ContentRoute("big.bin"), strings.Repeat("x", 2048))
	expectStatus(t, r, 413)
	if r.errCode(t) != api.CodePayloadTooLarge {
		t.Fatal(string(r.body))
	}
	// Chunked body (no Content-Length): cut off once the limit is crossed.
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(strings.Repeat("y", 4096)))
		_ = pw.Close()
	}()
	r = h.do("PUT", api.ContentRoute("chunked.bin"), h.ts.AdminKey, pr)
	expectStatus(t, r, 413)
	expectStatus(t, h.admin("GET", api.ObjectRoute("chunked.bin"), ""), 404)
	// Exactly at the limit is fine.
	expectStatus(t, h.admin("PUT", api.ContentRoute("ok.bin"), strings.Repeat("z", 1024)), 201)
}

func TestListPatchDeleteMoveTrash(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.upload("docs/a.pdf", "a", api.HeaderVisibility, "public")
	h.upload("docs/sub/b.pdf", "b")
	h.upload("top.txt", "t")

	r := h.admin("GET", api.RouteObject+"?prefix=docs/", "")
	expectStatus(t, r, 200)
	var l api.Listing
	r.json(t, &l)
	if l.Prefix != "docs/" || strings.Join(l.Dirs, ",") != "docs/sub/" || len(l.Objects) != 1 {
		t.Fatalf("listing = %+v", l)
	}
	r = h.admin("GET", api.RouteObject+"?recursive=true&visibility=unlisted&limit=1", "")
	r.json(t, &l)
	if len(l.Objects) != 1 || l.NextCursor == "" {
		t.Fatalf("filtered page = %+v", l)
	}
	r = h.admin("GET", api.RouteObject+"?recursive=true&visibility=unlisted&limit=1&cursor="+l.NextCursor, "")
	r.json(t, &l)
	if len(l.Objects) != 1 || l.Objects[0].Path != "top.txt" || l.NextCursor != "" {
		t.Fatalf("page 2 = %+v", l)
	}
	for _, q := range []string{"?recursive=maybe", "?limit=x", "?limit=0x", "?visibility=hidden"} {
		expectStatus(t, h.admin("GET", api.RouteObject+q, ""), 400)
	}

	r = h.admin("PATCH", api.ObjectRoute("top.txt"), `{"visibility":"private","title":"T"}`, "If-Match", `"r1"`)
	expectStatus(t, r, 200)
	if r.header.Get("ETag") != `"r2"` {
		t.Fatalf("patch ETag = %q", r.header.Get("ETag"))
	}
	expectStatus(t, h.admin("PATCH", api.ObjectRoute("top.txt"), `{"title":"x"}`, "If-Match", `"r1"`), 412)
	r = h.admin("PATCH", api.ObjectRoute("top.txt"), `{"path":"elsewhere"}`)
	if r.errCode(t) != api.CodeInvalidRequest {
		t.Fatal(string(r.body))
	}

	r = h.admin("DELETE", api.ObjectRoute("docs/"), "")
	if r.status != 400 || !strings.Contains(string(r.body), "recursive") {
		t.Fatalf("directory delete without recursive: %d %s", r.status, r.body)
	}
	r = h.admin("POST", api.RouteMove, `{"from":"docs/","to":"archive/","recursive":true}`)
	expectStatus(t, r, 200)
	var mv api.MoveResponse
	r.json(t, &mv)
	if mv.Moved != 2 {
		t.Fatalf("moved = %d", mv.Moved)
	}
	h.upload("docs/a.pdf", "conflict")
	r = h.admin("POST", api.RouteMove, `{"from":"archive/","to":"docs/","recursive":true}`)
	expectStatus(t, r, 409)
	var e api.ErrorResponse
	r.json(t, &e)
	if c, _ := e.Error.Details["conflicts"].([]any); len(c) != 1 || c[0] != "docs/a.pdf" {
		t.Fatalf("conflicts = %+v", e.Error.Details)
	}

	r = h.admin("DELETE", api.ObjectRoute("archive/")+"?recursive=true", "")
	expectStatus(t, r, 200)
	var del api.DeleteResponse
	r.json(t, &del)
	if del.Deleted != 2 || len(del.TrashIDs) != 2 {
		t.Fatalf("delete = %+v", del)
	}

	r = h.admin("GET", api.RouteTrash+"?prefix=archive/", "")
	var trash api.TrashList
	r.json(t, &trash)
	if len(trash.Entries) != 2 {
		t.Fatalf("trash = %+v", trash)
	}
	r = h.admin("POST", api.RouteTrash+"/"+trash.Entries[0].ID+"/restore", "")
	expectStatus(t, r, 200)
	r = h.admin("POST", api.RouteTrash+"/"+trash.Entries[1].ID+"/restore", `{"path":"restored.pdf"}`)
	expectStatus(t, r, 200)
	expectStatus(t, h.admin("GET", api.ObjectRoute("restored.pdf"), ""), 200)

	h.upload("gone.txt", "g")
	h.admin("DELETE", api.ObjectRoute("gone.txt"), "")
	r = h.admin("GET", api.RouteTrash+"?path=gone.txt", "")
	r.json(t, &trash)
	expectStatus(t, h.admin("DELETE", api.RouteTrash+"/"+trash.Entries[0].ID, ""), 204)
	expectStatus(t, h.admin("DELETE", api.RouteTrash+"/"+trash.Entries[0].ID, ""), 404)
	h.upload("gone2.txt", "g")
	h.admin("DELETE", api.ObjectRoute("gone2.txt"), "")
	expectStatus(t, h.admin("DELETE", api.RouteTrash+"?older_than=bogus", ""), 400)
	r = h.admin("DELETE", api.RouteTrash+"?older_than=1h", "")
	var purged api.PurgeResponse
	r.json(t, &purged)
	if purged.Purged != 0 {
		t.Fatalf("purged recent entries: %+v", purged)
	}
	r = h.admin("DELETE", api.RouteTrash, "")
	r.json(t, &purged)
	if purged.Purged != 1 {
		t.Fatalf("purge all = %+v", purged)
	}
	r = h.admin("DELETE", api.ObjectRoute("restored.pdf")+"?purge=true", "")
	expectStatus(t, r, 200)
}

func TestScopesAndPrefixes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.upload("docs/a.pdf", "a")
	h.upload("home/b.pdf", "b")
	ro := h.ts.Key(t, "ro", api.ScopeRO, "")
	rw := h.ts.Key(t, "docs", api.ScopeRW, "docs/")

	expectStatus(t, h.do("GET", api.ObjectRoute("home/b.pdf"), ro, nil), 200)
	expectStatus(t, h.do("PUT", api.ContentRoute("x"), ro, strings.NewReader("x")), 403)
	expectStatus(t, h.do("GET", api.RouteLinks, ro, nil), 403)
	expectStatus(t, h.do("GET", api.RouteTrash, ro, nil), 403)

	expectStatus(t, h.do("GET", api.ObjectRoute("home/b.pdf"), rw, nil), 403)
	expectStatus(t, h.do("PUT", api.ContentRoute("home/c"), rw, strings.NewReader("x")), 403)
	expectStatus(t, h.do("PUT", api.ContentRoute("docs/c"), rw, strings.NewReader("x")), 201)
	expectStatus(t, h.do("POST", api.RouteMove, rw, strings.NewReader(`{"from":"docs/c","to":"home/c"}`)), 403)
	expectStatus(t, h.do("DELETE", api.ObjectRoute("docs/c")+"?purge=true", rw, nil), 403)
	r := h.do("GET", api.RouteObject, rw, nil)
	var l api.Listing
	r.json(t, &l)
	if strings.Join(l.Dirs, ",") != "docs/" || len(l.Objects) != 0 {
		t.Fatalf("prefixed root listing = %+v", l)
	}
	expectStatus(t, h.do("GET", api.RouteObject+"?prefix=home/", rw, nil), 403)
}

// maxMetadataHeaders returns upload headers with the largest metadata the
// API accepts, in four-byte characters.
func maxMetadataHeaders() []string {
	tags := make([]string, 32)
	for i := range tags {
		tags[i] = fmt.Sprintf("%02d", i) + strings.Repeat("t", 62)
	}
	return []string{
		api.HeaderTitle, api.EncodeHeaderText(strings.Repeat("😀", api.MaxTitleRunes)),
		api.HeaderDescription, api.EncodeHeaderText(strings.Repeat("😀", api.MaxDescriptionRunes)),
		api.HeaderTags, strings.Join(tags, ","),
	}
}

// TestMaximalMetadataFitsProductionLimits covers R-014 for velad itself:
// the production http.Server accepts the largest valid metadata.
func TestMaximalMetadataFitsProductionLimits(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := h.ts.Server.HTTPServer("")
	go func() { _ = hs.Serve(ln) }()
	defer func() { _ = hs.Close() }()
	path := strings.Repeat("ж", 120) + "/" + strings.Repeat("ё", 120) + "/" + strings.Repeat("я", 120) + ".txt"
	req, _ := http.NewRequest("PUT", "http://"+ln.Addr().String()+api.ContentRoute(path), strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer "+h.ts.AdminKey)
	hdr := maxMetadataHeaders()
	for i := 0; i < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("maximal metadata: %d", res.StatusCode)
	}
}

// TestStalledBodiesAreDropped covers R-011: a client that announces a body
// and never sends it cannot hold a connection open past BodyIdleTimeout,
// whatever answers the request.
func TestStalledBodiesAreDropped(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testutil.WithServerOptions(func(o *server.Options) { o.BodyIdleTimeout = 100 * time.Millisecond }))
	addr := strings.TrimPrefix(h.ts.URL, "http://")
	requests := []string{
		"GET /healthz HTTP/1.1\r\nHost: x\r\nContent-Length: 1\r\n\r\n",
		"GET /api/v1/info HTTP/1.1\r\nHost: x\r\nContent-Length: 1\r\n\r\n",
		"POST /index.html HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n",
		"PUT /api/v1/content/x HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + h.ts.AdminKey + "\r\nX-Vela-Visibility: bogus\r\nContent-Length: 5\r\n\r\n",
		"PUT /api/v1/content/y HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + h.ts.AdminKey + "\r\nTransfer-Encoding: chunked\r\n\r\n",
	}
	for _, raw := range requests {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		start := time.Now()
		_, err = io.ReadAll(conn)
		_ = conn.Close()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Errorf("connection still open after %v: %q", time.Since(start), strings.SplitN(raw, "\r\n", 2)[0])
		}
	}
}

// TestCreateLimitIsAboutContent covers R-015: content at MaxCreateBytes is
// accepted however much its JSON encoding inflates it, and one byte more is
// refused, in both encodings.
func TestCreateLimitIsAboutContent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const limit = 64 << 10 // testutil's MaxCreate
	create := func(path string, content []byte, encoding string) resp {
		body, _ := json.Marshal(map[string]string{"path": path, "content": string(content), "encoding": encoding})
		if encoding == "base64" {
			body, _ = json.Marshal(map[string]string{"path": path, "content": base64.StdEncoding.EncodeToString(content), "encoding": encoding})
		}
		return h.admin("POST", api.RouteObject, string(body))
	}
	for name, b := range map[string]byte{"newlines": '\n', "quotes": '"', "controls": 0x01, "angles": '<'} {
		content := bytes.Repeat([]byte{b}, limit)
		if r := create("max-"+name, content, "utf-8"); r.status != 201 {
			t.Errorf("%s at the limit (utf-8): %d %.100s", name, r.status, r.body)
		}
		if r := create("b64-"+name, content, "base64"); r.status != 201 {
			t.Errorf("%s at the limit (base64): %d", name, r.status)
		}
		over := append(content, b)
		for _, enc := range []string{"utf-8", "base64"} {
			r := create("over-"+name+enc, over, enc)
			if r.status != 413 || r.errCode(t) != api.CodePayloadTooLarge {
				t.Errorf("%s over the limit (%s): %d", name, enc, r.status)
			}
		}
	}
}

// TestInvalidJSONTextIsRejected covers R-016: invalid UTF-8 and unpaired
// surrogate escapes are refused instead of being replaced with U+FFFD.
func TestInvalidJSONTextIsRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, body := range []string{
		"{\"path\":\"bad.txt\",\"content\":\"a\xffb\"}",
		`{"path":"bad.txt","content":"\ud800"}`,
		"{\"path\":\"bad\xff.txt\",\"content\":\"x\"}",
		`{"path":"bad.txt","content":"x","title":"\udfff"}`,
	} {
		r := h.admin("POST", api.RouteObject, body)
		if r.status != 400 || r.errCode(t) != api.CodeInvalidRequest {
			t.Errorf("POST %q: %d %s", body, r.status, r.body)
		}
	}
	if r := h.admin("PATCH", api.ObjectRoute("x"), "{\"title\":\"\xc3\"}"); r.status != 400 {
		t.Errorf("PATCH with invalid UTF-8: %d", r.status)
	}
	r := h.admin("POST", api.RouteObject, `{"path":"ok.txt","content":"\ud83d\ude00 \uFFFD"}`)
	if r.status != 201 {
		t.Fatalf("valid escapes: %d %s", r.status, r.body)
	}
	if r := h.admin("GET", api.ContentRoute("ok.txt"), ""); string(r.body) != "😀 \uFFFD" {
		t.Fatalf("stored %q", r.body)
	}
}

func TestListByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.upload("docs/a.pdf", "a", api.HeaderTags, "math,draft")
	h.upload("docs/b.pdf", "b", api.HeaderTags, "math")
	h.upload("home/c.pdf", "c", api.HeaderTags, "q1")
	h.upload("top.txt", "t")

	list := func(key, query string) string {
		t.Helper()
		r := h.do("GET", api.RouteObject+"?recursive=true&"+query, key, nil)
		expectStatus(t, r, 200)
		var l api.Listing
		r.json(t, &l)
		var got []string
		for _, o := range l.Objects {
			got = append(got, o.Path)
		}
		return strings.Join(got, " ")
	}
	admin := h.ts.AdminKey
	if got := list(admin, "tag=MATH,draft&tag=q1"); got != "docs/a.pdf home/c.pdf" {
		t.Errorf("tag filter = %q", got)
	}
	// The filter narrows a key's view; it never widens it.
	home := h.ts.Key(t, "home", api.ScopeRO, "home/")
	if got := list(home, "tag=math&tag=q1"); got != "home/c.pdf" {
		t.Errorf("prefixed key = %q", got)
	}
	expectStatus(t, h.admin("GET", api.RouteObject+"?tag=a/b", ""), 400)
	expectStatus(t, h.admin("GET", api.RouteObject+"?tag=", ""), 400)
}

func TestTrashListByTag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.upload("a.pdf", "a", api.HeaderTags, "math,draft")
	h.upload("b.pdf", "b", api.HeaderTags, "math")
	h.upload("c.pdf", "c")
	for _, p := range []string{"a.pdf", "b.pdf", "c.pdf"} {
		expectStatus(t, h.admin("DELETE", api.ObjectRoute(p), ""), 200)
	}
	r := h.admin("GET", api.RouteTrash+"?tag=draft,math&tag=nope", "")
	expectStatus(t, r, 200)
	var l api.TrashList
	r.json(t, &l)
	if len(l.Entries) != 1 || l.Entries[0].OriginalPath != "a.pdf" {
		t.Fatalf("trash by tag = %+v", l.Entries)
	}
	expectStatus(t, h.admin("GET", api.RouteTrash+"?tag=bad%20tag%2F", ""), 400)
}
