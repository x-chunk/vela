// Package config loads the velad server configuration.
//
// Every setting comes from an environment variable (convenient for
// "docker run --env-file") and can be overridden by a flag of "velad serve".
// Precedence: flag, then environment, then default. An environment variable
// that is set but empty counts as set, because some settings are
// meaningfully empty: VELA_ACCEL_PREFIX="" selects direct delivery.
//
// Load validates everything up front so that a misconfigured server refuses
// to start with a clear message instead of failing on the first request.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/units"
)

// Robots policies for /robots.txt.
const (
	RobotsAllow = "allow"
	RobotsDeny  = "deny"
)

// Config is the validated server configuration.
type Config struct {
	Storage              string
	R2                   R2Config
	MaxConcurrentUploads int
	Listen               string
	DataDir              string
	PublicURL            string
	AccelPrefix          string
	TrustedProxies       []netip.Prefix
	MaxUpload            int64
	MaxCreate            int64
	LinkDefaultTTL       time.Duration
	LinkMaxTTL           time.Duration
	LinkRetention        time.Duration
	TrashRetention       time.Duration
	JanitorInterval      time.Duration
	RootIndex            api.IndexMode
	SiteTitle            string
	ThemeDir             string
	LogLevel             slog.Level
	LogFormat            string
	DefaultVisibility    api.Visibility
	Robots               string
	// WebAuth enables the read-only browser sign-in with an API key.
	WebAuth bool
	// WebInsecureCookie drops Secure and the __Host- prefix from the session
	// cookie, for plain-HTTP development only.
	WebInsecureCookie bool
	WebSessionIdle    time.Duration
	WebSessionMax     time.Duration
}

type setting struct {
	env, flag, def, usage string
	apply                 func(c *Config, v string) error
}

func durationSetting(dst func(*Config) *time.Duration) func(*Config, string) error {
	return func(c *Config, v string) error {
		d, err := api.ParseDuration(v)
		if err != nil {
			return err
		}
		*dst(c) = d
		return nil
	}
}

func sizeSetting(dst func(*Config) *int64) func(*Config, string) error {
	return func(c *Config, v string) error {
		n, err := units.ParseSize(v)
		if err != nil {
			return err
		}
		*dst(c) = n
		return nil
	}
}

// switchSetting accepts on/off and the usual boolean spellings, so that both
// VELA_WEB_AUTH=on and VELA_WEB_INSECURE_COOKIE=1 read naturally.
func switchSetting(dst func(*Config) *bool) func(*Config, string) error {
	return func(c *Config, v string) error {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "on", "true", "yes", "1":
			*dst(c) = true
		case "off", "false", "no", "0":
			*dst(c) = false
		default:
			return fmt.Errorf("%q must be on or off", v)
		}
		return nil
	}
}

func stringSetting(dst func(*Config) *string) func(*Config, string) error {
	return func(c *Config, v string) error {
		*dst(c) = strings.TrimSpace(v)
		return nil
	}
}

// settings is the single table of core configuration knobs. The environment
// variable table in docs/configuration.md must match it.
var settings = []setting{
	{"VELA_ROOT_INDEX", "root-index", "on", "default root HTML index: on or off (overridden by a database rule)", parseRootIndex},
	{"VELA_LISTEN", "listen", ":8080", "HTTP listen address", stringSetting(func(c *Config) *string { return &c.Listen })},
	{"VELA_DATA_DIR", "data-dir", "/data", "data directory (database, blobs, tmp)", stringSetting(func(c *Config) *string { return &c.DataDir })},
	{"VELA_PUBLIC_URL", "public-url", "", "public base URL, e.g. https://store.example.com (required to serve)", parsePublicURL},
	{"VELA_ACCEL_PREFIX", "accel-prefix", "", "nginx X-Accel-Redirect prefix; empty serves files directly", parseAccelPrefix},
	{"VELA_TRUSTED_PROXIES", "trusted-proxies", "127.0.0.1/32,::1/128", "CIDRs whose X-Real-IP/X-Forwarded-For/X-Request-ID are trusted", parseProxies},
	{"VELA_MAX_UPLOAD", "max-upload", "2GiB", "maximum upload size (keep in sync with nginx client_max_body_size)", sizeSetting(func(c *Config) *int64 { return &c.MaxUpload })},
	{"VELA_MAX_CREATE", "max-create", "1MiB", "maximum size of inline content for POST /api/v1/objects", sizeSetting(func(c *Config) *int64 { return &c.MaxCreate })},
	{"VELA_LINK_DEFAULT_TTL", "link-default-ttl", "24h", "default signed link lifetime", durationSetting(func(c *Config) *time.Duration { return &c.LinkDefaultTTL })},
	{"VELA_LINK_MAX_TTL", "link-max-ttl", "30d", "maximum signed link lifetime", durationSetting(func(c *Config) *time.Duration { return &c.LinkMaxTTL })},
	{"VELA_LINK_RETENTION", "link-retention", "7d", "how long expired link records are kept", durationSetting(func(c *Config) *time.Duration { return &c.LinkRetention })},
	{"VELA_TRASH_RETENTION", "trash-retention", "14d", "how long deleted objects stay in the trash", durationSetting(func(c *Config) *time.Duration { return &c.TrashRetention })},
	{"VELA_JANITOR_INTERVAL", "janitor-interval", "24h", "interval between janitor passes", durationSetting(func(c *Config) *time.Duration { return &c.JanitorInterval })},
	{"VELA_SITE_TITLE", "site-title", "Vela", "title of the HTML index", stringSetting(func(c *Config) *string { return &c.SiteTitle })},
	{"VELA_THEME_DIR", "theme-dir", "", "directory with theme overrides for the HTML index", stringSetting(func(c *Config) *string { return &c.ThemeDir })},
	{"VELA_LOG_LEVEL", "log-level", "info", "log level: debug, info, warn or error", parseLogLevel},
	{"VELA_LOG_FORMAT", "log-format", "json", "log format: json or text", parseLogFormat},
	{"VELA_DEFAULT_VISIBILITY", "default-visibility", "unlisted", "visibility of new objects when the client sets none", parseVisibility},
	{"VELA_ROBOTS", "robots", RobotsDeny, "robots.txt policy: deny (Disallow: /) or allow", parseRobots},
	{"VELA_WEB_AUTH", "web-auth", "off", "read-only web sign-in with an API key: on or off", switchSetting(func(c *Config) *bool { return &c.WebAuth })},
	{"VELA_WEB_INSECURE_COOKIE", "web-insecure-cookie", "off", "send the web session cookie without Secure (plain-HTTP development only)", switchSetting(func(c *Config) *bool { return &c.WebInsecureCookie })},
	{"VELA_WEB_SESSION_IDLE", "web-session-idle", "7d", "web session idle timeout", durationSetting(func(c *Config) *time.Duration { return &c.WebSessionIdle })},
	{"VELA_WEB_SESSION_MAX", "web-session-max", "30d", "absolute web session lifetime", durationSetting(func(c *Config) *time.Duration { return &c.WebSessionMax })},
}

// Common flags are registered on every velad command; the rest only on
// "serve".
var commonFlags = map[string]bool{"data-dir": true, "log-level": true, "log-format": true}

// RegisterFlags adds the flags for all settings (all=true, for "serve") or
// only the common ones (data directory and logging) to fs.
func RegisterFlags(fs *pflag.FlagSet, all bool) {
	for _, s := range settings {
		if all || commonFlags[s.flag] {
			fs.String(s.flag, s.def, fmt.Sprintf("%s (env %s)", s.usage, s.env))
		}
	}
}

// Load resolves and validates the configuration. fs may be nil or lack some
// flags; lookupEnv is normally os.LookupEnv.
func Load(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*Config, error) {
	c := &Config{}
	var errs []error
	for _, s := range settings {
		v := s.def
		if ev, ok := lookupEnv(s.env); ok {
			v = ev
		}
		if fs != nil && fs.Changed(s.flag) {
			fv, err := fs.GetString(s.flag)
			if err != nil {
				return nil, err
			}
			v = fv
		}
		if err := s.apply(c, v); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.env, err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := c.loadStorage(lookupEnv); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	check(c.Listen != "", "VELA_LISTEN must not be empty")
	check(c.DataDir != "", "VELA_DATA_DIR must not be empty")
	check(c.MaxUpload > 0, "VELA_MAX_UPLOAD must be positive")
	check(c.MaxCreate > 0, "VELA_MAX_CREATE must be positive")
	check(c.LinkDefaultTTL > 0, "VELA_LINK_DEFAULT_TTL must be positive")
	check(c.LinkDefaultTTL <= c.LinkMaxTTL, "VELA_LINK_DEFAULT_TTL (%s) must not exceed VELA_LINK_MAX_TTL (%s)",
		api.FormatDuration(c.LinkDefaultTTL), api.FormatDuration(c.LinkMaxTTL))
	check(c.TrashRetention > 0, "VELA_TRASH_RETENTION must be positive")
	check(c.JanitorInterval >= time.Minute, "VELA_JANITOR_INTERVAL must be at least 1m")
	check(c.SiteTitle != "", "VELA_SITE_TITLE must not be empty")
	check(c.WebSessionIdle >= time.Minute, "VELA_WEB_SESSION_IDLE must be at least 1m")
	check(c.WebSessionIdle <= c.WebSessionMax, "VELA_WEB_SESSION_IDLE (%s) must not exceed VELA_WEB_SESSION_MAX (%s)",
		api.FormatDuration(c.WebSessionIdle), api.FormatDuration(c.WebSessionMax))
	return errors.Join(errs...)
}

// RequireServe checks the settings that only "velad serve" needs.
func (c *Config) RequireServe() error {
	if c.Storage == "r2" && c.AccelPrefix != "" {
		return errors.New("VELA_ACCEL_PREFIX must be empty when VELA_STORAGE=r2")
	}
	if c.PublicURL == "" {
		return errors.New("VELA_PUBLIC_URL is required (for example https://store.example.com)")
	}
	// Browsers drop a Secure cookie set over plain HTTP, so sign-in would
	// appear to succeed and then silently stay anonymous.
	if c.WebAuth && !c.WebInsecureCookie && !strings.HasPrefix(c.PublicURL, "https://") {
		return errors.New("VELA_WEB_AUTH needs an https VELA_PUBLIC_URL; set VELA_WEB_INSECURE_COOKIE=on only for plain-HTTP development")
	}
	return nil
}

// AccelEnabled reports whether content is delivered through nginx.
func (c *Config) AccelEnabled() bool { return c.AccelPrefix != "" }

// Logger builds the process logger.
func (c *Config) Logger(w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func parsePublicURL(c *Config, v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		c.PublicURL = ""
		return nil
	}
	u, err := url.Parse(v)
	if err != nil {
		return err
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%q must be an absolute http(s) URL", v)
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%q must be a bare origin without path, query or credentials", v)
	}
	c.PublicURL = u.Scheme + "://" + u.Host
	return nil
}

func parseAccelPrefix(c *Config, v string) error {
	v = strings.TrimSpace(v)
	if v != "" && (!strings.HasPrefix(v, "/") || !strings.HasSuffix(v, "/")) {
		return fmt.Errorf("%q must start and end with a slash, or be empty", v)
	}
	c.AccelPrefix = v
	return nil
}

func parseProxies(c *Config, v string) error {
	c.TrustedProxies = nil
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var p netip.Prefix
		var err error
		if strings.Contains(part, "/") {
			p, err = netip.ParsePrefix(part)
		} else {
			var a netip.Addr
			a, err = netip.ParseAddr(part)
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", part, err)
		}
		c.TrustedProxies = append(c.TrustedProxies, p.Masked())
	}
	return nil
}

func parseLogLevel(c *Config, v string) error {
	return c.LogLevel.UnmarshalText([]byte(strings.TrimSpace(v)))
}

func parseLogFormat(c *Config, v string) error {
	v = strings.ToLower(strings.TrimSpace(v))
	if v != "json" && v != "text" {
		return fmt.Errorf("%q is not json or text", v)
	}
	c.LogFormat = v
	return nil
}

func parseVisibility(c *Config, v string) error {
	vis, err := api.ParseVisibility(v)
	c.DefaultVisibility = vis
	return err
}

func parseRobots(c *Config, v string) error {
	v = strings.ToLower(strings.TrimSpace(v))
	if v != RobotsAllow && v != RobotsDeny {
		return fmt.Errorf("%q is not allow or deny", v)
	}
	c.Robots = v
	return nil
}

func parseRootIndex(c *Config, v string) error {
	c.RootIndex = api.IndexMode(strings.TrimSpace(v))
	if c.RootIndex != api.IndexOn && c.RootIndex != api.IndexOff {
		return fmt.Errorf("%q must be on or off", v)
	}
	return nil
}
