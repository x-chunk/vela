// Package units parses and formats byte sizes for configuration, the HTML
// index and CLI output.
package units

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Binary (IEC) units.
const (
	KiB int64 = 1 << 10
	MiB int64 = 1 << 20
	GiB int64 = 1 << 30
	TiB int64 = 1 << 40
)

var sizeUnits = map[string]int64{
	"":    1,
	"b":   1,
	"k":   KiB,
	"kib": KiB,
	"kb":  1000,
	"m":   MiB,
	"mib": MiB,
	"mb":  1000 * 1000,
	"g":   GiB,
	"gib": GiB,
	"gb":  1000 * 1000 * 1000,
	"t":   TiB,
	"tib": TiB,
	"tb":  1000 * 1000 * 1000 * 1000,
}

// ParseSize parses sizes such as "2GiB", "1MiB", "500KB", "2g" or "1024".
// Single-letter suffixes are binary, matching nginx's client_max_body_size
// ("2g" means 2 GiB), so the two configs can be kept in sync literally.
func ParseSize(s string) (int64, error) {
	in := strings.TrimSpace(s)
	i := 0
	for i < len(in) && in[i] >= '0' && in[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid size %q: expected a number", s)
	}
	n, err := strconv.ParseInt(in[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	mult, ok := sizeUnits[strings.ToLower(strings.TrimSpace(in[i:]))]
	if !ok {
		return 0, fmt.Errorf("invalid size %q: unknown unit %q", s, in[i:])
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("invalid size %q: too large", s)
	}
	return n * mult, nil
}

// FormatSize renders n bytes with binary units and one decimal place:
// "512 B", "1.5 KiB", "2.0 GiB".
func FormatSize(n int64) string {
	if n < KiB {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	v := float64(n)
	i := -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
