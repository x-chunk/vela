package mimetype

import "testing"

func TestDetect(t *testing.T) {
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
		if got := Detect(tt.name, []byte(tt.sniff)); got != tt.want {
			t.Errorf("Detect(%q, %q) = %q; want %q", tt.name, tt.sniff, got, tt.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"text/plain":                     "text/plain; charset=utf-8",
		"Text/Markdown":                  "text/markdown; charset=utf-8",
		"text/plain; charset=iso-8859-1": "text/plain; charset=iso-8859-1",
		"application/pdf":                "application/pdf",
		"application/json":               "application/json; charset=utf-8",
	}
	for in, want := range tests {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "pdf", "text/", "a b/c"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) succeeded", bad)
		}
	}
	if !IsGeneric("") || !IsGeneric("application/octet-stream") || IsGeneric("text/plain") {
		t.Error("IsGeneric mismatch")
	}
}
