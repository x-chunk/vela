package api

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeTags(t *testing.T) {
	t.Parallel()
	got, err := NormalizeTags([]string{"Math", " q1 ", "math", "v1.2_x-y"})
	if err != nil || strings.Join(got, ",") != "math,q1,v1.2_x-y" {
		t.Fatalf("NormalizeTags = %v, %v", got, err)
	}
	if got, err := NormalizeTags(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("NormalizeTags(nil) = %#v, %v; want empty non-nil slice", got, err)
	}
	for _, bad := range [][]string{{""}, {"has space"}, {"тег"}, {strings.Repeat("a", 65)}, {"a/b"}} {
		if _, err := NormalizeTags(bad); err == nil {
			t.Errorf("NormalizeTags(%q) succeeded", bad)
		}
	}
	many := make([]string, 33)
	for i := range many {
		many[i] = strings.Repeat("t", i+1)
	}
	if _, err := NormalizeTags(many); err == nil {
		t.Error("33 tags accepted")
	}
}

func TestValidateText(t *testing.T) {
	t.Parallel()
	if err := ValidateTitle(strings.Repeat("я", 256)); err != nil {
		t.Errorf("256-rune title rejected: %v", err)
	}
	if err := ValidateTitle(strings.Repeat("я", 257)); err == nil {
		t.Error("257-rune title accepted")
	}
	if err := ValidateDescription(strings.Repeat("x", 4097)); err == nil {
		t.Error("long description accepted")
	}
}

func TestParseExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 12, 0, 0, 500, time.UTC)
	got, err := ParseExpiry("7d", now)
	if err != nil || !got.Equal(time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("ParseExpiry(7d) = %v, %v", got, err)
	}
	got, err = ParseExpiry("2026-12-31T23:00:00+03:00", now)
	if err != nil || !got.Equal(time.Date(2026, 12, 31, 20, 0, 0, 0, time.UTC)) || got.Location() != time.UTC {
		t.Fatalf("ParseExpiry(rfc3339) = %v, %v", got, err)
	}
	if got, err := ParseExpiry("None", now); err != nil || got != nil {
		t.Fatalf("ParseExpiry(none) = %v, %v", got, err)
	}
	for _, bad := range []string{"", "0", "tomorrow", "2026-13-01"} {
		if _, err := ParseExpiry(bad, now); err == nil {
			t.Errorf("ParseExpiry(%q) succeeded", bad)
		}
	}
}
