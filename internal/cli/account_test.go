package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/cli/config"
	"go.xchunk.org/vela/internal/testutil"
)

func TestLoginLogoutWhoami(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.config = filepath.Join(t.TempDir(), "fresh", "config.toml")
	r := h.exec(h.ts.AdminKey+"\n", "login", "--url", h.ts.URL, "--profile", "work")
	if r.code != 0 || !strings.Contains(r.stdout, `as "admin"`) {
		t.Fatalf("login = %+v", r)
	}
	fi, err := os.Stat(h.config)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", fi.Mode().Perm())
	}
	r = h.ok("whoami", "-v")
	if !strings.Contains(r.stdout, "work") || !strings.Contains(r.stdout, "_****") || strings.Contains(r.stdout, h.ts.AdminKey) {
		t.Fatalf("whoami = %s", r.stdout)
	}
	var info api.Info
	h.ok("whoami", "--json").json(t, &info)
	if info.Key.Name != "admin" {
		t.Fatalf("whoami --json = %+v", info)
	}
	if r := h.exec("not a key\n", "login", "--url", h.ts.URL); r.code != exitUsage {
		t.Fatalf("bad key accepted: %+v", r)
	}
	h.ok("version")
	h.ok("logout")
	if r := h.exec("", "whoami"); r.code != exitAuth {
		t.Fatalf("whoami after logout = %+v", r)
	}
}

func TestConfigPermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits on Windows")
	}
	h := newHarness(t)
	if err := os.Chmod(h.config, 0o644); err != nil {
		t.Fatal(err)
	}
	r := h.exec("", "whoami")
	if r.code != exitError || !strings.Contains(r.stderr, "chmod 600 "+h.config) {
		t.Fatalf("open config accepted: %+v", r)
	}
}

// TestLoginHonorsVelaURL covers R-008: VELA_URL (and --url over it) decides
// where login sends the key, also when a profile points elsewhere.
func TestLoginHonorsVelaURL(t *testing.T) {
	t.Parallel()
	h := newHarness(t) // profile "main" points at h.ts
	other := testutil.NewServer(t)
	h.env["VELA_URL"] = other.URL
	if r := h.exec(other.AdminKey+"\n", "login"); r.code != 0 {
		t.Fatalf("login with VELA_URL: %+v", r)
	}
	f, err := config.Load(h.config)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Profiles["main"].URL; got != other.URL {
		t.Fatalf("saved URL = %s; want %s", got, other.URL)
	}
	// --url wins over VELA_URL.
	if r := h.exec(h.ts.AdminKey+"\n", "login", "--url", h.ts.URL); r.code != 0 {
		t.Fatalf("login with --url: %+v", r)
	}
	f, _ = config.Load(h.config)
	if got := f.Profiles["main"].URL; got != h.ts.URL {
		t.Fatalf("saved URL = %s; want %s", got, h.ts.URL)
	}
	// A key for one server sent to the other is rejected, not saved.
	h.env["VELA_URL"] = other.URL
	if r := h.exec(h.ts.AdminKey+"\n", "login"); r.code != exitAuth {
		t.Fatalf("wrong key for VELA_URL: %+v", r)
	}
}
