package service

import (
	"testing"

	"go.xchunk.org/vela/api"
)

func TestEffectiveDirectoryPolicy(t *testing.T) {
	rules := map[string]api.DirPolicy{}
	add := func(prefix string, index api.IndexMode, hidden bool, noindex api.IndexMode) {
		rules[prefix] = api.DirPolicy{Prefix: prefix, Index: index, Hidden: hidden, NoIndex: noindex}
	}
	add("", api.IndexOff, false, api.IndexInherit)
	add("door/", api.IndexOn, true, api.IndexInherit)
	add("door/pass/", api.IndexInherit, false, api.IndexInherit)
	add("door/pass/raw/", api.IndexOff, false, api.IndexInherit)
	add("door/search/", api.IndexInherit, false, api.IndexOff)
	add("door/search/deep/", api.IndexInherit, true, api.IndexInherit)
	add("visible/", api.IndexInherit, false, api.IndexOn)
	rules["door/"] = func() api.DirPolicy { p := rules["door/"]; p.Title = "Door"; p.Description = "Description"; return p }()
	for _, tc := range []struct {
		prefix       string
		index        api.IndexMode
		indexSource  string
		hidden       bool
		robots       api.IndexMode
		robotsSource string
	}{
		{"", api.IndexOff, "", false, api.IndexOff, "default"},
		{"other/", api.IndexOff, "", false, api.IndexOff, "default"},
		{"door/", api.IndexOn, "door/", true, api.IndexOn, "hidden:door/"},
		{"door/pass/deep/", api.IndexOn, "door/", false, api.IndexOn, "hidden:door/"},
		{"door/pass/raw/sub/", api.IndexOff, "door/pass/raw/", false, api.IndexOn, "hidden:door/"},
		{"door/search/", api.IndexOn, "door/", false, api.IndexOff, "door/search/"},
		{"door/search/deep/", api.IndexOn, "door/", true, api.IndexOff, "door/search/"},
		{"visible/sub/", api.IndexOff, "", false, api.IndexOn, "visible/"},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			view := &DirPolicies{rules: rules, rootDefault: api.IndexOn}
			got := view.Effective(tc.prefix)
			if got.Index != tc.index || got.IndexSource != tc.indexSource || got.Hidden != tc.hidden || got.NoIndex != tc.robots || got.NoIndexSource != tc.robotsSource {
				t.Fatalf("effective: %+v", got)
			}
			if tc.prefix == "door/" {
				if got.Title != "Door" || got.Description != "Description" {
					t.Fatal(got)
				}
			} else if got.Title != "" || got.Description != "" {
				t.Fatal("cosmetic fields inherited")
			}
		})
	}
	for _, def := range []api.IndexMode{api.IndexOn, api.IndexOff} {
		view := &DirPolicies{rules: map[string]api.DirPolicy{}, rootDefault: def}
		if got := view.Effective("a/b/"); got.Index != def || got.IndexSource != "config" {
			t.Fatal(got)
		}
		view.rules[""] = api.DirPolicy{Index: api.IndexInherit, NoIndex: api.IndexInherit}
		if got := view.Effective(""); got.Index != def || got.IndexSource != "config" {
			t.Fatal(got)
		}
		view.rules[""] = api.DirPolicy{Index: api.IndexOn, NoIndex: api.IndexInherit}
		if got := view.Effective("a/"); got.Index != api.IndexOn || got.IndexSource != "" {
			t.Fatal(got)
		}
	}
}
