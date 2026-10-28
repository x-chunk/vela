package api

import (
	"slices"
	"strings"
	"testing"
)

func TestParseTagFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   []string
		want TagFilter
		bad  bool
	}{
		{in: nil, want: nil},
		{in: []string{"Q1"}, want: TagFilter{{"q1"}}},
		{in: []string{"b a", "a,b", "c"}, want: TagFilter{{"a", "b"}, {"c"}}},
		{in: []string{" math\tdraft  "}, want: TagFilter{{"draft", "math"}}},
		{in: []string{""}, bad: true},
		{in: []string{" , "}, bad: true},
		{in: []string{"a/b"}, bad: true},
	}
	for _, tt := range tests {
		got, err := ParseTagFilter(tt.in)
		if tt.bad {
			if err == nil {
				t.Errorf("ParseTagFilter(%q) succeeded", tt.in)
			}
			continue
		}
		if err != nil || !slices.EqualFunc(got, tt.want, slices.Equal) {
			t.Errorf("ParseTagFilter(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	many := make([]string, MaxTagGroups+1)
	for i := range many {
		many[i] = strings.Repeat("t", i+1)
	}
	if _, err := ParseTagFilter(many); err == nil {
		t.Error("too many groups accepted")
	}
}

func TestTagFilterMatch(t *testing.T) {
	t.Parallel()
	f := TagFilter{{"q1"}, {"draft", "math"}}
	tests := []struct {
		tags []string
		want bool
	}{
		{[]string{"q1"}, true},
		{[]string{"math"}, false},
		{[]string{"math", "x", "draft"}, true},
		{nil, false},
	}
	for _, tt := range tests {
		if got := f.Match(tt.tags); got != tt.want {
			t.Errorf("Match(%q) = %v", tt.tags, got)
		}
	}
	if !TagFilter(nil).Match(nil) {
		t.Error("empty filter must match everything")
	}
	if got := strings.Join(f.Values(), "|"); got != "q1|draft,math" {
		t.Errorf("Values = %q", got)
	}
	if got := f.String(); got != "q1 or (draft and math)" {
		t.Errorf("String = %q", got)
	}
}
