package api

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanObjectPath(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 255)
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "a.pdf", want: "a.pdf"},
		{in: "/docs/a.pdf", want: "docs/a.pdf"},
		{in: "///docs/a.pdf", want: "docs/a.pdf"},
		{in: "docs/math/q1/test.pdf", want: "docs/math/q1/test.pdf"},
		{in: "A.pdf", want: "A.pdf"},
		{in: "dir with space/file name.txt", want: "dir with space/file name.txt"},
		{in: "кириллица/файл.pdf", want: "кириллица/файл.pdf"},
		// NFD "é" (e + combining acute) is folded to NFC.
		{in: "café.txt", want: "café.txt"},
		{in: "q?x#y%z.txt", want: "q?x#y%z.txt"},
		{in: "api-docs/x", want: "api-docs/x"},
		{in: "docs/api/x", want: "docs/api/x"},
		{in: long, want: long},

		{in: "", wantErr: "must not end with a slash"},
		{in: "/", wantErr: "must not end with a slash"},
		{in: "docs/", wantErr: "must not end with a slash"},
		{in: "a//b", wantErr: "empty segment"},
		{in: "a/./b", wantErr: "dot segment"},
		{in: "a/../b", wantErr: "dot segment"},
		{in: "..", wantErr: "dot segment"},
		{in: `a\b`, wantErr: "backslash"},
		{in: "a\x00b", wantErr: "control character"},
		{in: "a\nb", wantErr: "control character"},
		{in: "a\x7fb", wantErr: "control character"},
		{in: "a\u0085b", wantErr: "control character"},
		{in: " a", wantErr: "whitespace"},
		{in: "a /b", wantErr: "whitespace"},
		{in: "a/ b", wantErr: "whitespace"},
		{in: "   ", wantErr: "whitespace"},
		{in: "api/x", wantErr: "reserved"},
		{in: "API/x", wantErr: "reserved"},
		{in: "s/x", wantErr: "reserved"},
		{in: "_vela", wantErr: "reserved"},
		{in: "_Blobs/x", wantErr: "reserved"},
		{in: "healthz", wantErr: "reserved"},
		{in: "robots.txt", wantErr: "reserved"},
		{in: "Favicon.ICO", wantErr: "reserved"},
		{in: long + "a", wantErr: "segment longer"},
		{in: strings.Repeat("abcd/", 205) + "x", wantErr: "longer than 1024"},
		{in: "bad\xffutf8", wantErr: "UTF-8"},
	}
	for _, tt := range tests {
		got, err := CleanObjectPath(tt.in)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("CleanObjectPath(%q) = %q, %v; want error containing %q", tt.in, got, err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidPath) {
				t.Errorf("CleanObjectPath(%q) error %v does not wrap ErrInvalidPath", tt.in, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("CleanObjectPath(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestCleanPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"", ""},
		{"/", ""},
		{"docs", "docs/"},
		{"docs/", "docs/"},
		{"/docs/math/", "docs/math/"},
	}
	for _, tt := range tests {
		got, err := CleanPrefix(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("CleanPrefix(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"docs//", "api/", "a/../"} {
		if _, err := CleanPrefix(bad); err == nil {
			t.Errorf("CleanPrefix(%q) succeeded, want error", bad)
		}
	}
}

func TestCleanPath(t *testing.T) {
	t.Parallel()
	p, dir, err := CleanPath("/docs/")
	if err != nil || p != "docs/" || !dir {
		t.Fatalf("CleanPath(/docs/) = %q, %v, %v", p, dir, err)
	}
	p, dir, err = CleanPath("docs/a")
	if err != nil || p != "docs/a" || dir {
		t.Fatalf("CleanPath(docs/a) = %q, %v, %v", p, dir, err)
	}
	p, dir, err = CleanPath("")
	if err != nil || p != "" || !dir {
		t.Fatalf("CleanPath(\"\") = %q, %v, %v", p, dir, err)
	}
}

func TestPathHelpers(t *testing.T) {
	t.Parallel()
	if !IsWithin("docs/a", "docs/") || IsWithin("education/a", "docs/") || !IsWithin("x", "") || IsWithin("docs", "docs/") {
		t.Error("IsWithin mismatch")
	}
	if got := BaseName("docs/math/q1.pdf"); got != "q1.pdf" {
		t.Errorf("BaseName = %q", got)
	}
	if got := BaseName("docs/math/"); got != "math" {
		t.Errorf("BaseName(dir) = %q", got)
	}
	if got := ParentPrefix("docs/math/q1.pdf"); got != "docs/math/" {
		t.Errorf("ParentPrefix = %q", got)
	}
	if got := ParentPrefix("top.txt"); got != "" {
		t.Errorf("ParentPrefix(top) = %q", got)
	}
	if got := ParentPrefix("docs/math/"); got != "docs/" {
		t.Errorf("ParentPrefix(dir) = %q", got)
	}
	if got := strings.Join(Ancestors("a/b/c"), ","); got != "a,a/b" {
		t.Errorf("Ancestors = %q", got)
	}
	if got := len(Ancestors("a")); got != 0 {
		t.Errorf("Ancestors(a) has %d entries", got)
	}
}

func TestEscapePath(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"docs/a b.pdf", "docs/a%20b.pdf"},
		{"q?x#y%z.txt", "q%3Fx%23y%25z.txt"},
		{"файл", "%D1%84%D0%B0%D0%B9%D0%BB"},
		{"docs/math/", "docs/math/"},
	}
	for _, tt := range tests {
		if got := EscapePath(tt.in); got != tt.want {
			t.Errorf("EscapePath(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
	if got := PublicURL("https://store.example.com/", "a b/c"); got != "https://store.example.com/a%20b/c" {
		t.Errorf("PublicURL = %q", got)
	}
}
