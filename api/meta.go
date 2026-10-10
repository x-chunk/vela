package api

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Metadata limits from spec section 10.2.
const (
	MaxTags             = 32
	MaxTagLength        = 64
	MaxTitleRunes       = 256
	MaxDescriptionRunes = 4096
)

// NormalizeTags lower-cases, validates and de-duplicates tags, preserving the
// order of first occurrence. Allowed characters are [a-z0-9-_.].
func NormalizeTags(tags []string) ([]string, error) {
	out := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, raw := range tags {
		t := strings.ToLower(strings.TrimSpace(raw))
		if t == "" {
			return nil, fmt.Errorf("empty tag")
		}
		if len(t) > MaxTagLength {
			return nil, fmt.Errorf("tag %q is longer than %d characters", t, MaxTagLength)
		}
		for _, r := range t {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return nil, fmt.Errorf("tag %q contains %q (allowed: a-z, 0-9, '-', '_', '.')", t, r)
			}
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) > MaxTags {
		return nil, fmt.Errorf("too many tags: %d (max %d)", len(out), MaxTags)
	}
	return out, nil
}

// ValidateTitle checks the title length limit.
func ValidateTitle(s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("title is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(s); n > MaxTitleRunes {
		return fmt.Errorf("title is %d characters long (max %d)", n, MaxTitleRunes)
	}
	return nil
}

// ValidateDescription checks the description length limit.
func ValidateDescription(s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("description is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(s); n > MaxDescriptionRunes {
		return fmt.Errorf("description is %d characters long (max %d)", n, MaxDescriptionRunes)
	}
	return nil
}

// ParseExpiry parses a user-facing expiry: a duration relative to now ("7d"),
// an RFC 3339 timestamp, or "none" to clear the expiry. It returns nil for
// "none". Results are truncated to whole seconds and converted to UTC.
func ParseExpiry(s string, now time.Time) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, ExpiresNone) {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		t = t.UTC().Truncate(time.Second)
		return &t, nil
	}
	d, err := ParseDuration(s)
	if err != nil {
		return nil, fmt.Errorf("invalid expiry %q: want a duration like 7d, an RFC 3339 time or %q", s, ExpiresNone)
	}
	if d <= 0 {
		return nil, fmt.Errorf("invalid expiry %q: duration must be positive", s)
	}
	t := now.Add(d).UTC().Truncate(time.Second)
	return &t, nil
}
