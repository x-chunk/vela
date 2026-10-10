package ids

import (
	"testing"
	"time"
)

func TestULID(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	g := NewULID(func() time.Time { return ts })
	seen := map[string]bool{}
	for range 1000 {
		id := g.New()
		if !Valid(id) {
			t.Fatalf("invalid id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	later := NewULID(func() time.Time { return ts.Add(time.Millisecond) }).New()
	for id := range seen {
		if id >= later {
			t.Fatalf("ULIDs are not time ordered: %q >= %q", id, later)
		}
	}
	for _, bad := range []string{"", "01JABC", "01JAB0000000000000000000000", "01JABCDEFGHJKMNPQRSTVWXYZ0", "01jabcdefghjkmnpqrstvwxyzu"} {
		if Valid(bad) {
			t.Errorf("Valid(%q) = true", bad)
		}
	}
}
