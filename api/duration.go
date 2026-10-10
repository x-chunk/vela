package api

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Day and Week are the calendar-free units accepted by ParseDuration. They are
// fixed multiples of 24 hours; daylight saving time is deliberately ignored
// because every timestamp in Vela is UTC.
const (
	Day  = 24 * time.Hour
	Week = 7 * Day
)

var durationUnits = map[string]time.Duration{
	"s": time.Second,
	"m": time.Minute,
	"h": time.Hour,
	"d": Day,
	"w": Week,
}

// ParseDuration parses human durations such as "90m", "2h", "7d", "2w" or
// combinations like "1d12h". Unlike time.ParseDuration it understands days and
// weeks, and it rejects negative values and fractions, which have no meaning
// for TTLs and retention periods. "0" is accepted and means zero.
func ParseDuration(s string) (time.Duration, error) {
	in := strings.TrimSpace(s)
	if in == "0" {
		return 0, nil
	}
	if in == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	rest := in
	for rest != "" {
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("invalid duration %q: expected a number", s)
		}
		n, err := strconv.ParseInt(rest[:i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
		rest = rest[i:]
		j := 0
		for j < len(rest) && (rest[j] < '0' || rest[j] > '9') {
			j++
		}
		unit, ok := durationUnits[strings.ToLower(rest[:j])]
		if !ok {
			return 0, fmt.Errorf("invalid duration %q: unknown unit %q (use s, m, h, d or w)", s, rest[:j])
		}
		rest = rest[j:]
		if n > int64(math.MaxInt64/unit) {
			return 0, fmt.Errorf("invalid duration %q: too large", s)
		}
		part := time.Duration(n) * unit
		if total > math.MaxInt64-part {
			return 0, fmt.Errorf("invalid duration %q: too large", s)
		}
		total += part
	}
	return total, nil
}

// FormatDuration renders d with hours as the largest unit and without zero
// components: 720h, 24h, 1h30m, 45s. Sub-second precision is dropped. The
// output is always accepted by ParseDuration and by time.ParseDuration.
func FormatDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	d = d.Truncate(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	sec := d / time.Second
	var b strings.Builder
	if h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	if sec > 0 {
		fmt.Fprintf(&b, "%ds", sec)
	}
	return b.String()
}
