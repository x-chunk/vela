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
	if c.Listen != ":8080" || c.DataDir != "/data" || c.AccelPrefix != "/_blobs/" || c.MaxUpload != 2<<30 ||
		c.MaxCreate != 1<<20 || c.LinkMaxTTL != 30*api.Day || c.TrashRetention != 14*api.Day ||
		c.DefaultVisibility != api.Unlisted || c.Robots != RobotsDeny || c.LogLevel != slog.LevelInfo || c.LogFormat != "json" {
		t.Fatalf("defaults = %+v", c)
	}
	want := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("172.17.0.1/32")}
	if len(c.TrustedProxies) != 3 || c.TrustedProxies[2] != want[2] {
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
		"VELA_PUBLIC_URL":   "https://store.xchunk.org/",
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
	if c.PublicURL != "https://store.xchunk.org" || c.Robots != RobotsAllow || c.LogLevel != slog.LevelDebug {
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
		"must be an absolute":        {"VELA_PUBLIC_URL": "store.xchunk.org"},
		"bare origin":                {"VELA_PUBLIC_URL": "https://store.xchunk.org/files"},
		"start and end with a slash": {"VELA_ACCEL_PREFIX": "_blobs"},
		"invalid CIDR":               {"VELA_TRUSTED_PROXIES": "10.0.0.0/33"},
		"VELA_MAX_UPLOAD":            {"VELA_MAX_UPLOAD": "lots"},
		"must not exceed":            {"VELA_LINK_DEFAULT_TTL": "31d"},
		"at least 1m":                {"VELA_JANITOR_INTERVAL": "30s"},
		"unknown visibility":         {"VELA_DEFAULT_VISIBILITY": "secret"},
		"not allow or deny":          {"VELA_ROBOTS": "maybe"},
		"not json or text":           {"VELA_LOG_FORMAT": "xml"},
		"VELA_LOG_LEVEL":             {"VELA_LOG_LEVEL": "loud"},
		"VELA_TRASH_RETENTION":       {"VELA_TRASH_RETENTION": "0"},
		"VELA_LINK_RETENTION":        {"VELA_LINK_RETENTION": "-1d"},
	}
	for want, e := range tests {
		_, err := Load(nil, env(e))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: error = %v; want containing %q", e, err, want)
		}
	}
	c, err := Load(nil, env(map[string]string{"VELA_TRUSTED_PROXIES": "10.0.0.5, 192.168.1.0/24", "VELA_JANITOR_INTERVAL": "1h"}))
	if err != nil || len(c.TrustedProxies) != 2 || c.TrustedProxies[0].Bits() != 32 || c.JanitorInterval != time.Hour {
		t.Fatalf("single address proxy = %+v, %v", c, err)
	}
}
