package config

import (
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"go.xchunk.org/vela/api"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestDefaults(t *testing.T) {
	t.Parallel()
	c, err := Load(nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.DataDir != "/data" || c.AccelPrefix != "" || c.SiteTitle != "Vela" || c.MaxUpload != 2<<30 ||
		c.MaxCreate != 1<<20 || c.LinkMaxTTL != 30*api.Day || c.TrashRetention != 14*api.Day ||
		c.DefaultVisibility != api.Unlisted || c.Robots != RobotsDeny || c.LogLevel != slog.LevelInfo || c.LogFormat != "json" {
		t.Fatalf("defaults = %+v", c)
	}
	want := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}
	if len(c.TrustedProxies) != len(want) || c.TrustedProxies[0] != want[0] || c.TrustedProxies[1] != want[1] {
		t.Fatalf("trusted proxies = %v", c.TrustedProxies)
	}
	if err := c.RequireServe(); err == nil {
		t.Fatal("serve accepted a missing public URL")
	}
}

func TestPrecedence(t *testing.T) {
	t.Parallel()
	fs := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	RegisterFlags(fs, true)
	if err := fs.Parse([]string{"--listen", "127.0.0.1:9000", "--accel-prefix="}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(fs, env(map[string]string{
		"VELA_LISTEN":       ":7000",
		"VELA_PUBLIC_URL":   "https://store.example.com/",
		"VELA_ACCEL_PREFIX": "/x/",
		"VELA_ROBOTS":       "Allow",
		"VELA_LOG_LEVEL":    "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9000" || c.AccelPrefix != "" || c.AccelEnabled() {
		t.Fatalf("flags did not win: %+v", c)
	}
	if c.PublicURL != "https://store.example.com" || c.Robots != RobotsAllow || c.LogLevel != slog.LevelDebug {
		t.Fatalf("env not applied: %+v", c)
	}
	if err := c.RequireServe(); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyEnvCountsAsSet(t *testing.T) {
	t.Parallel()
	c, err := Load(nil, env(map[string]string{"VELA_ACCEL_PREFIX": ""}))
	if err != nil || c.AccelEnabled() {
		t.Fatalf("empty VELA_ACCEL_PREFIX did not select direct mode: %+v, %v", c, err)
	}
}

func TestCommonFlagsOnly(t *testing.T) {
	t.Parallel()
	fs := pflag.NewFlagSet("key", pflag.ContinueOnError)
	RegisterFlags(fs, false)
	if fs.Lookup("data-dir") == nil || fs.Lookup("listen") != nil {
		t.Fatal("wrong flag subset")
	}
	if err := fs.Parse([]string{"--data-dir", "/tmp/x"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(fs, env(nil))
	if err != nil || c.DataDir != "/tmp/x" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
}

func TestValidation(t *testing.T) {
	t.Parallel()
	tests := map[string]map[string]string{
		"must be an absolute":        {"VELA_PUBLIC_URL": "store.example.com"},
		"bare origin":                {"VELA_PUBLIC_URL": "https://store.example.com/files"},
		"start and end with a slash": {"VELA_ACCEL_PREFIX": "_blobs"},
		"invalid CIDR":               {"VELA_TRUSTED_PROXIES": "192.0.2.0/33"},
		"VELA_MAX_UPLOAD":            {"VELA_MAX_UPLOAD": "lots"},
		"must not exceed":            {"VELA_LINK_DEFAULT_TTL": "31d"},
		"at least 1m":                {"VELA_JANITOR_INTERVAL": "30s"},
		"unknown visibility":         {"VELA_DEFAULT_VISIBILITY": "secret"},
		"not allow or deny":          {"VELA_ROBOTS": "maybe"},
		"not json or text":           {"VELA_LOG_FORMAT": "xml"},
		"VELA_LOG_LEVEL":             {"VELA_LOG_LEVEL": "loud"},
		"VELA_TRASH_RETENTION":       {"VELA_TRASH_RETENTION": "0"},
		"VELA_LINK_RETENTION":        {"VELA_LINK_RETENTION": "-1d"},
		"VELA_WEB_AUTH":              {"VELA_WEB_AUTH": "maybe"},
		"VELA_WEB_INSECURE_COOKIE":   {"VELA_WEB_INSECURE_COOKIE": "2"},
		"IDLE must be at least 1m":   {"VELA_WEB_SESSION_IDLE": "30s"},
		"must not exceed VELA_WEB":   {"VELA_WEB_SESSION_IDLE": "8d", "VELA_WEB_SESSION_MAX": "7d"},
	}
	for want, e := range tests {
		_, err := Load(nil, env(e))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: error = %v; want containing %q", e, err, want)
		}
	}
	c, err := Load(nil, env(map[string]string{"VELA_TRUSTED_PROXIES": "192.0.2.5, 198.51.100.0/24", "VELA_JANITOR_INTERVAL": "1h"}))
	if err != nil || len(c.TrustedProxies) != 2 || c.TrustedProxies[0].Bits() != 32 || c.JanitorInterval != time.Hour {
		t.Fatalf("single address proxy = %+v, %v", c, err)
	}
}

func TestRootIndex(t *testing.T) {
	for _, value := range []string{"on", "off", "inherit", "", "true"} {
		t.Run(value, func(t *testing.T) {
			cfg, err := Load(nil, func(name string) (string, bool) { return value, name == "VELA_ROOT_INDEX" })
			valid := value == "on" || value == "off"
			if valid && (err != nil || string(cfg.RootIndex) != value) {
				t.Fatalf("root index: %+v %v", cfg, err)
			}
			if !valid && err == nil {
				t.Fatal("invalid root default accepted")
			}
		})
	}
}

func TestWebAuth(t *testing.T) {
	t.Parallel()
	c, err := Load(nil, env(map[string]string{"VELA_PUBLIC_URL": "https://store.example.com"}))
	if err != nil || c.WebAuth || c.WebInsecureCookie || c.WebSessionIdle != 7*api.Day || c.WebSessionMax != 30*api.Day {
		t.Fatalf("web defaults = %+v, %v", c, err)
	}
	for _, tc := range []struct {
		env   map[string]string
		serve bool
	}{
		{map[string]string{"VELA_WEB_AUTH": "on", "VELA_PUBLIC_URL": "https://store.example.com"}, true},
		{map[string]string{"VELA_WEB_AUTH": "ON", "VELA_PUBLIC_URL": "http://127.0.0.1:8080"}, false},
		{map[string]string{"VELA_WEB_AUTH": "true", "VELA_WEB_INSECURE_COOKIE": "1", "VELA_PUBLIC_URL": "http://127.0.0.1:8080"}, true},
		{map[string]string{"VELA_WEB_AUTH": "off", "VELA_PUBLIC_URL": "http://127.0.0.1:8080"}, true},
	} {
		c, err := Load(nil, env(tc.env))
		if err != nil {
			t.Fatalf("%v: %v", tc.env, err)
		}
		if err := c.RequireServe(); (err == nil) != tc.serve {
			t.Fatalf("%v: RequireServe = %v", tc.env, err)
		}
	}
	c, err = Load(nil, env(map[string]string{"VELA_WEB_AUTH": "yes", "VELA_WEB_SESSION_IDLE": "2h", "VELA_WEB_SESSION_MAX": "2h"}))
	if err != nil || !c.WebAuth || c.WebSessionIdle != 2*time.Hour || c.WebSessionMax != 2*time.Hour {
		t.Fatalf("web settings = %+v, %v", c, err)
	}
}
