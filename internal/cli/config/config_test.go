package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "vela", "config.toml")
	f := &File{DefaultProfile: "main", Profiles: map[string]*Profile{
		"main": {URL: "https://store.xchunk.org", Key: "vela_k_secret", DefaultVisibility: "private"},
		"edu":  {URL: "https://store.xchunk.org", Key: "vela_e_secret"},
	}}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			fi, _ := os.Stat(p)
			if fi.Mode().Perm() != want {
				t.Errorf("%s mode = %o; want %o", p, fi.Mode().Perm(), want)
			}
		}
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultProfile != "main" || got.Profiles["edu"].Key != "vela_e_secret" || got.Profiles["main"].DefaultVisibility != "private" {
		t.Fatalf("round trip = %+v", got)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "[profiles.main]") {
		t.Fatalf("unexpected TOML layout:\n%s", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

func TestLoadRejectsOpenPermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("default_profile = \"main\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	var pe *PermissionError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "chmod 600 "+path) {
		t.Fatalf("Load = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	missing, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil || len(missing.Profiles) != 0 {
		t.Fatalf("missing file = %+v, %v", missing, err)
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	f := &File{DefaultProfile: "main", Profiles: map[string]*Profile{
		"main": {URL: "https://main.example", Key: "kmain", DefaultVisibility: "public"},
		"edu":  {URL: "https://edu.example", Key: "kedu"},
	}}
	s, err := Resolve(f, "", "", env(nil))
	if err != nil || s.Profile != "main" || s.URL != "https://main.example" || s.Key != "kmain" || s.DefaultVisibility != api.Public {
		t.Fatalf("default = %+v, %v", s, err)
	}
	s, _ = Resolve(f, "", "", env(map[string]string{"VELA_PROFILE": "edu", "VELA_KEY": "kenv"}))
	if s.Profile != "edu" || s.URL != "https://edu.example" || s.Key != "kenv" {
		t.Fatalf("env = %+v", s)
	}
	s, _ = Resolve(f, "edu", "https://flag.example", env(map[string]string{"VELA_PROFILE": "main", "VELA_URL": "https://env.example"}))
	if s.Profile != "edu" || s.URL != "https://flag.example" {
		t.Fatalf("flags = %+v", s)
	}
	if _, err := Resolve(f, "nope", "", env(nil)); err == nil {
		t.Fatal("unknown explicit profile accepted")
	}
	single := &File{Profiles: map[string]*Profile{"only": {URL: "u", Key: "k"}}}
	if s, _ := Resolve(single, "", "", env(nil)); s.Profile != "only" {
		t.Fatalf("single profile = %+v", s)
	}
	empty := &File{Profiles: map[string]*Profile{}}
	s, err = Resolve(empty, "", "", env(map[string]string{"VELA_URL": "https://x", "VELA_KEY": "k"}))
	if err != nil || s.URL != "https://x" || s.Key != "k" {
		t.Fatalf("env-only = %+v, %v", s, err)
	}
	bad := &File{Profiles: map[string]*Profile{"main": {DefaultVisibility: "secret"}}}
	if _, err := Resolve(bad, "", "", env(nil)); err == nil {
		t.Fatal("bad default_visibility accepted")
	}
}
