package server

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/clock"
)

func TestAuthLimiter(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	l := newAuthLimiter(clk.Now)
	ip := netip.MustParseAddr("203.0.113.7")
	other := netip.MustParseAddr("203.0.113.8")
	for i := 0; i < authBurst; i++ {
		if b, _ := l.blocked(ip); b {
			t.Fatalf("blocked after %d failures", i)
		}
		l.fail(ip)
	}
	b, wait := l.blocked(ip)
	if !b || wait <= 0 || wait > 7*time.Second {
		t.Fatalf("after burst: blocked=%v wait=%v", b, wait)
	}
	if b, _ := l.blocked(other); b {
		t.Fatal("limit leaked to another IP")
	}
	clk.Advance(6 * time.Second) // one token at 10/min
	if b, _ := l.blocked(ip); b {
		t.Fatal("still blocked after refill")
	}
	clk.Advance(2 * time.Minute)
	l.fail(other) // triggers a sweep
	l.mu.Lock()
	_, kept := l.buckets[ip]
	l.mu.Unlock()
	if kept {
		t.Fatal("fully refilled bucket not swept")
	}
}

func TestForwardedIP(t *testing.T) {
	t.Parallel()
	s := &Server{opts: Options{TrustedProxies: []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("172.17.0.1/32"),
	}}}
	tests := []struct {
		name, remote, realIP, xff, want string
	}{
		{"untrusted peer ignores headers", "198.51.100.1:1234", "192.0.2.4", "192.0.2.8", "198.51.100.1"},
		{"trusted peer, X-Real-IP", "172.17.0.1:5555", "192.0.2.4", "", "192.0.2.4"},
		{"trusted peer, XFF rightmost untrusted", "127.0.0.1:1", "", "203.0.113.9, 192.0.2.4, 172.17.0.1", "192.0.2.4"},
		{"trusted peer, no headers", "127.0.0.1:1", "", "", "127.0.0.1"},
		{"trusted peer, garbage", "127.0.0.1:1", "nope", "also nope", "127.0.0.1"},
		{"ipv6 peer", "[2001:db8::1]:443", "192.0.2.4", "", "2001:db8::1"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tt.remote
		if tt.realIP != "" {
			r.Header.Set("X-Real-IP", tt.realIP)
		}
		if tt.xff != "" {
			r.Header.Set("X-Forwarded-For", tt.xff)
		}
		peer := remoteAddr(r)
		got := peer
		if s.trusted(peer) {
			got = forwardedIP(r, peer, s.trusted)
		}
		if got.String() != tt.want {
			t.Errorf("%s: got %s; want %s", tt.name, got, tt.want)
		}
	}
}

func TestContentDisposition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		d    api.Disposition
		name string
		want string
	}{
		{api.Inline, "test.pdf", `inline; filename="test.pdf"; filename*=UTF-8''test.pdf`},
		{api.Attachment, "Контрольная Q1.pdf", `attachment; filename="___________ Q1.pdf"; filename*=UTF-8''%D0%9A%D0%BE%D0%BD%D1%82%D1%80%D0%BE%D0%BB%D1%8C%D0%BD%D0%B0%D1%8F%20Q1.pdf`},
		{api.Inline, `a"b\c%d.txt`, `inline; filename="a_b_c_d.txt"; filename*=UTF-8''a%22b%5Cc%25d.txt`},
		{"", "x", `inline; filename="x"; filename*=UTF-8''x`},
	}
	for _, tt := range tests {
		if got := contentDisposition(tt.d, tt.name); got != tt.want {
			t.Errorf("contentDisposition(%q, %q)\n got %s\nwant %s", tt.d, tt.name, got, tt.want)
		}
	}
}

func TestCleanURLPath(t *testing.T) {
	t.Parallel()
	for p, want := range map[string]bool{
		"/api/v1/objects":        true,
		"/api/v1/objects/":       true,
		"/api/v1/objects/a/b":    true,
		"/api/v1/objects/a//b":   false,
		"/api/v1/objects/a/./b":  false,
		"/api/v1/objects/a/../b": false,
		"/api/v1/objects/a/..":   false,
		"/api/v1/objects/a/b/":   true,
	} {
		if got := cleanURLPath(p); got != want {
			t.Errorf("cleanURLPath(%q) = %v; want %v", p, got, want)
		}
	}
}

func TestCheckJSONText(t *testing.T) {
	t.Parallel()
	ok := []string{`{"a":"plain"}`, `{"a":"\ud83d\ude00"}`, `{"a":"\uFFFD"}`, `{"a":"\\ud800"}`, `{"a":"é"}`}
	bad := []string{
		"{\"a\":\"\xff\"}", `{"a":"\ud800"}`, `{"a":"\udc00"}`, `{"a":"\ud800\u0041"}`, `{"a":"x\ud83d"}`,
		// An escaped quote must not end the string for the scanner.
		`{"a":"\u00e9","b":"\"\ud800"}`,
	}
	for _, s := range ok {
		if err := checkJSONText([]byte(s)); err != nil {
			t.Errorf("checkJSONText(%s) = %v", s, err)
		}
	}
	for _, s := range bad {
		if err := checkJSONText([]byte(s)); err == nil {
			t.Errorf("checkJSONText(%q) accepted", s)
		}
	}
}
