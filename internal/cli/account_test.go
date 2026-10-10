package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
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
