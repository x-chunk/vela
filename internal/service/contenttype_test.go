package service

import "testing"

func TestDetectContentType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		sniff string
		want  string
	}{
		{"a.pdf", "", "application/pdf"},
		{"notes.MD", "", "text/markdown; charset=utf-8"},
		{"data.json", "", "application/json; charset=utf-8"},
		{"logo.svg", "", "image/svg+xml; charset=utf-8"},
		{"page.html", "", "text/html; charset=utf-8"},
		{"noext", "%PDF-1.7\n", "application/pdf"},
		{"noext", "hello world", "text/plain; charset=utf-8"},
		{"noext", "\x00\x01\x02\x03", "application/octet-stream"},
		{"archive.tar.gz", "", "application/gzip"},
	}
	for _, tt := range tests {
		if got := detectContentType(tt.name, []byte(tt.sniff)); got != tt.want {
			t.Errorf("detectContentType(%q, %q) = %q; want %q", tt.name, tt.sniff, got, tt.want)
		}
	}
}

func TestNormalizeContentType(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"text/plain":                     "text/plain; charset=utf-8",
		"Text/Markdown":                  "text/markdown; charset=utf-8",
		"text/plain; charset=iso-8859-1": "text/plain; charset=iso-8859-1",
		"application/pdf":                "application/pdf",
		"application/json":               "application/json; charset=utf-8",
	}
	for in, want := range tests {
		got, err := normalizeContentType(in)
		if err != nil || got != want {
			t.Errorf("normalizeContentType(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "pdf", "text/", "a b/c"} {
		if _, err := normalizeContentType(bad); err == nil {
			t.Errorf("normalizeContentType(%q) succeeded", bad)
		}
	}
	if !isGenericContentType("") || !isGenericContentType("application/octet-stream") || isGenericContentType("text/plain") {
		t.Error("isGenericContentType mismatch")
	}
}
