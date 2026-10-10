package api

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"0", 0},
		{"45s", 45 * time.Second},
		{"90m", 90 * time.Minute},
		{"2h", 2 * time.Hour},
		{"7d", 7 * Day},
		{"2w", 14 * Day},
		{"1d12h", 36 * time.Hour},
		{"1h30m", 90 * time.Minute},
		{" 24H ", 24 * time.Hour},
		{"720h", 30 * Day},
	}
	for _, tt := range tests {
		got, err := ParseDuration(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "d", "7", "-1h", "1.5h", "3y", "1h 30m", "99999999999999999999w", "9223372036w"} {
		if got, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) = %v, want error", bad, got)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{500 * time.Millisecond, "0s"},
		{24 * time.Hour, "24h"},
		{30 * Day, "720h"},
		{90 * time.Minute, "1h30m"},
		{time.Hour + 5*time.Second, "1h5s"},
		{45 * time.Second, "45s"},
	}
	for _, tt := range tests {
		got := FormatDuration(tt.in)
		if got != tt.want {
			t.Errorf("FormatDuration(%v) = %q; want %q", tt.in, got, tt.want)
		}
		if back, err := ParseDuration(got); err != nil || back != tt.in.Truncate(time.Second) {
			t.Errorf("round trip of %q = %v, %v", got, back, err)
		}
	}
}
