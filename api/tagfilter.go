package api

import (
	"fmt"
	"slices"
	"strings"
)

// MaxTagGroups caps the groups of a TagFilter, which bounds the SQL a
// listing builds from one query string.
const MaxTagGroups = 32

// TagFilter selects objects by their tags: an object matches when it carries
// every tag of at least one group (groups are OR-ed, tags within a group
// AND-ed). An empty filter matches everything.
type TagFilter [][]string

// ParseTagGroup parses one group of a TagFilter. Tags are separated by commas
// or whitespace, which tags cannot contain, and normalized like
// NormalizeTags.
func ParseTagGroup(s string) ([]string, error) {
	tags, err := NormalizeTags(strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}))
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("empty tag group")
	}
	return tags, nil
}

// ParseTagFilter parses groups as ParseTagGroup does and drops duplicates.
func ParseTagFilter(groups []string) (TagFilter, error) {
	var f TagFilter
	for _, raw := range groups {
		g, err := ParseTagGroup(raw)
		if err != nil {
			return nil, err
		}
		f = f.Or(g)
	}
	if len(f) > MaxTagGroups {
		return nil, fmt.Errorf("too many tag groups: %d (max %d)", len(f), MaxTagGroups)
	}
	return f, nil
}

// Or returns f with group added, unless f already has the same group.
func (f TagFilter) Or(group []string) TagFilter {
	g := slices.Clone(group)
	slices.Sort(g)
	for _, have := range f {
		if slices.Equal(have, g) {
			return f
		}
	}
	return append(f, g)
}

// Match reports whether tags satisfy f.
func (f TagFilter) Match(tags []string) bool {
	if len(f) == 0 {
		return true
	}
	for _, g := range f {
		if !slices.ContainsFunc(g, func(t string) bool { return !slices.Contains(tags, t) }) {
			return true
		}
	}
	return false
}

// Values encodes f as the values of the repeated "tag" query parameter, one
// comma-separated group each.
func (f TagFilter) Values() []string {
	out := make([]string, len(f))
	for i, g := range f {
		out[i] = strings.Join(g, ",")
	}
	return out
}

// String describes f for people, e.g. "q1 or (math and draft)".
func (f TagFilter) String() string {
	parts := make([]string, len(f))
	for i, g := range f {
		parts[i] = strings.Join(g, " and ")
		if len(g) > 1 && len(f) > 1 {
			parts[i] = "(" + parts[i] + ")"
		}
	}
	return strings.Join(parts, " or ")
}
