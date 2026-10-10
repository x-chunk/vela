package api

import "testing"

func TestParseVisibility(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]Visibility{"public": Public, "Unlisted": Unlisted, " PRIVATE ": Private} {
		got, err := ParseVisibility(in)
		if err != nil || got != want {
			t.Errorf("ParseVisibility(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "secret", "pub"} {
		if _, err := ParseVisibility(bad); err == nil {
			t.Errorf("ParseVisibility(%q) succeeded", bad)
		}
	}
}

func TestParseDisposition(t *testing.T) {
	t.Parallel()
	if d, err := ParseDisposition("Attachment"); err != nil || d != Attachment {
		t.Errorf("ParseDisposition = %q, %v", d, err)
	}
	if _, err := ParseDisposition("download"); err == nil {
		t.Error("ParseDisposition(download) succeeded")
	}
}

func TestScopeAllows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		have, need Scope
		want       bool
	}{
		{ScopeRO, ScopeRO, true},
		{ScopeRO, ScopeRW, false},
		{ScopeRW, ScopeRO, true},
		{ScopeRW, ScopeAdmin, false},
		{ScopeAdmin, ScopeRW, true},
		{Scope("root"), ScopeRO, false},
		{Scope(""), Scope(""), false},
	}
	for _, tt := range tests {
		if got := tt.have.Allows(tt.need); got != tt.want {
			t.Errorf("%q.Allows(%q) = %v; want %v", tt.have, tt.need, got, tt.want)
		}
	}
	if _, err := ParseScope("superuser"); err == nil {
		t.Error("ParseScope(superuser) succeeded")
	}
}
