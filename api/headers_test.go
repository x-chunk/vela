package api

import (
	"strings"
	"testing"
)

func TestRevisionETag(t *testing.T) {
	t.Parallel()
	if got := FormatRevisionETag(3); got != `"r3"` {
		t.Fatalf("FormatRevisionETag = %s", got)
	}
	if rev, err := ParseRevisionETag(` "r42" `); err != nil || rev != 42 {
		t.Fatalf("ParseRevisionETag = %d, %v", rev, err)
	}
	for _, bad := range []string{`r3`, `"3"`, `W/"r3"`, `"r"`, `"r0"`, `"r-1"`, `*`} {
		if _, err := ParseRevisionETag(bad); err == nil {
			t.Errorf("ParseRevisionETag(%s) succeeded", bad)
		}
	}
}

func TestHeaderText(t *testing.T) {
	t.Parallel()
	in := "Контрольная Q1, 50% / вариант"
	enc := EncodeHeaderText(in)
	for _, r := range enc {
		if r > 0x7e || r < 0x20 {
			t.Fatalf("encoded header has non-ASCII %q", enc)
		}
	}
	if out, err := DecodeHeaderText(enc); err != nil || out != in {
		t.Fatalf("round trip = %q, %v", out, err)
	}
	if got := ParseTagsHeader(" a, ,b,"); strings.Join(got, "|") != "a|b" {
		t.Fatalf("ParseTagsHeader = %q", got)
	}
}
