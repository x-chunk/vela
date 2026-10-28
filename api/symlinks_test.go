package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestParseObjectKind(t *testing.T) {
	t.Parallel()
	for in, ok := range map[string]bool{"file": true, "symlink": true, "": false, "link": false, "File": false} {
		k, err := ParseObjectKind(in)
		if (err == nil) != ok || (ok && string(k) != in) {
			t.Errorf("ParseObjectKind(%q) = %q, %v", in, k, err)
		}
	}
}

func TestIsSymlinkStatus(t *testing.T) {
	t.Parallel()
	if got := CodeIsSymlink.Status(); got != http.StatusConflict {
		t.Errorf("is_symlink status = %d", got)
	}
}

func TestSymlinkObjectJSON(t *testing.T) {
	t.Parallel()
	path := "orig/a.txt"
	vis := Unlisted
	o := Object{Kind: KindSymlink, Visibility: Public, EffectiveVisibility: Unlisted,
		Target: &SymlinkTarget{ID: "01t", Path: &path, State: TargetOK, Visibility: &vis}, Tags: []string{}}
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	target, _ := m["target"].(map[string]any)
	if m["kind"] != "symlink" || m["effective_visibility"] != "unlisted" || target["state"] != "ok" ||
		target["path"] != path || target["visibility"] != "unlisted" || target["expired"] != false {
		t.Errorf("JSON = %s", b)
	}
	b, _ = json.Marshal(Object{Kind: KindFile})
	if err := json.Unmarshal(b, &m); err != nil || m["target"] != nil {
		t.Errorf("file target must be null: %s", b)
	}
}

func TestInfoFeaturesFromOlderServer(t *testing.T) {
	t.Parallel()
	var info Info
	if err := json.Unmarshal([]byte(`{"api_version":1}`), &info); err != nil || info.Features.Symlinks {
		t.Errorf("older server info = %+v, %v", info, err)
	}
}
