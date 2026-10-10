package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestObjectPatchUnmarshal(t *testing.T) {
	t.Parallel()
	var p ObjectPatch
	err := json.Unmarshal([]byte(`{"visibility":"public","title":null,"tags":["a","b"],"expires_at":"2026-10-11T00:00:00Z"}`), &p)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Visibility.HasValue() || p.Visibility.Value != Public {
		t.Errorf("visibility = %+v", p.Visibility)
	}
	if !p.Title.Set || !p.Title.Null {
		t.Errorf("title = %+v, want null", p.Title)
	}
	if p.Description.Set || p.ContentType.Set || p.Disposition.Set {
		t.Error("absent fields marked as set")
	}
	if strings.Join(p.Tags.Value, ",") != "a,b" {
		t.Errorf("tags = %+v", p.Tags)
	}
	if !p.ExpiresAt.Value.Equal(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("expires_at = %+v", p.ExpiresAt)
	}
	if p.IsEmpty() {
		t.Error("IsEmpty on non-empty patch")
	}
}

func TestObjectPatchRejects(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		`{"path":"x"}`:          "cannot be changed",
		`{"size":1}`:            "unknown field",
		`[]`:                    "JSON object",
		`null`:                  "JSON object",
		`{"tags":"a"}`:          `field "tags"`,
		`{"expires_at":"soon"}`: `field "expires_at"`,
	}
	for in, want := range tests {
		var p ObjectPatch
		err := json.Unmarshal([]byte(in), &p)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Unmarshal(%s) error = %v; want containing %q", in, err, want)
		}
	}
}

func TestObjectPatchMarshal(t *testing.T) {
	t.Parallel()
	p := ObjectPatch{
		Visibility: Some(Private),
		ExpiresAt:  Null[time.Time](),
		Tags:       Some([]string{"x"}),
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"expires_at":null,"tags":["x"],"visibility":"private"}` {
		t.Fatalf("Marshal = %s", b)
	}
	var back ObjectPatch
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.ExpiresAt.Null || back.Visibility.Value != Private || back.Title.Set {
		t.Fatalf("round trip = %+v", back)
	}
	if !(ObjectPatch{}).IsEmpty() {
		t.Error("zero patch not empty")
	}
}
