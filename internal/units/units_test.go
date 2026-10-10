package units

import "testing"

func TestParseSize(t *testing.T) {
	t.Parallel()
	tests := map[string]int64{
		"0":     0,
		"1024":  1024,
		"10B":   10,
		"1KiB":  1024,
		"1kb":   1000,
		"1MiB":  1 << 20,
		"2GiB":  2 << 30,
		"2g":    2 << 30,
		"1 TiB": 1 << 40,
		" 5MB ": 5_000_000,
		"1024k": 1 << 20,
	}
	for in, want := range tests {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "GiB", "-1", "1.5GiB", "1PB", "9999999999999TiB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) succeeded", bad)
		}
	}
}

func TestFormatSize(t *testing.T) {
	t.Parallel()
	tests := map[int64]string{
		0:             "0 B",
		1023:          "1023 B",
		1024:          "1.0 KiB",
		1536:          "1.5 KiB",
		482193:        "470.9 KiB",
		2 << 30:       "2.0 GiB",
		5 * (1 << 40): "5.0 TiB",
	}
	for in, want := range tests {
		if got := FormatSize(in); got != want {
			t.Errorf("FormatSize(%d) = %q; want %q", in, got, want)
		}
	}
}
