// Package config manages the vela CLI configuration file: named profiles,
// each with a server URL and an API key, stored as TOML.
//
// Because the file holds API keys, the CLI refuses to read it when other
// users can access it (like ssh does with private keys), and always writes it
// privately through a temporary file and rename, so a crash never leaves a
// truncated or readable config behind. Privacy means mode 0600 on Unix and,
// on Windows, a protected DACL that grants access only to the user and
// SYSTEM (POSIX mode bits do not exist there; os.Chmod only toggles the
// read-only attribute).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"go.xchunk.org/vela/api"
)

// DefaultURL is offered by "vela login".
const DefaultURL = "https://store.xchunk.org"

// DefaultProfileName is used when nothing selects a profile.
const DefaultProfileName = "main"

// File is the on-disk configuration.
type File struct {
	DefaultProfile string              `toml:"default_profile,omitempty"`
	Profiles       map[string]*Profile `toml:"profiles,omitempty"`
}

// Profile is one server/key pair.
type Profile struct {
	URL string `toml:"url"`
	Key string `toml:"key"`
	// DefaultVisibility applies to put and new when no visibility flag is
	// given; empty means the server default.
	DefaultVisibility string `toml:"default_visibility,omitempty"`
}

// DefaultPath returns $XDG_CONFIG_HOME/vela/config.toml or the platform
// equivalent.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the config directory: %w", err)
	}
	return filepath.Join(dir, "vela", "config.toml"), nil
}

// PermissionError reports a config file that others can read or write.
type PermissionError struct {
	Path string
	// Mode is the file mode on Unix.
	Mode fs.FileMode
	// Others names who else has access, on Windows.
	Others string
}

func (e *PermissionError) Error() string {
	if e.Others != "" {
		return fmt.Sprintf("config file %s can be read by %s, but it contains API keys and must be private; "+
			"run: icacls \"%s\" /inheritance:r /grant:r \"%%USERNAME%%:F\"", e.Path, e.Others, e.Path)
	}
	return fmt.Sprintf("config file %s has permissions %04o, but it contains API keys and must be private; run: chmod 600 %s",
		e.Path, e.Mode.Perm(), e.Path)
}

// Load reads the file at path. A missing file yields an empty config. A
// file others can access is refused (see checkPrivate for each platform).
func Load(path string) (*File, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &File{Profiles: map[string]*Profile{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := checkPrivate(path, info); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path) // #nosec G304 -- the user's own config path.
	if err != nil {
		return nil, err
	}
	f := &File{}
	if err := toml.Unmarshal(b, f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Profiles == nil {
		f.Profiles = map[string]*Profile{}
	}
	return f, nil
}

// Save writes the file atomically and privately: the temporary file is
// made private before the key is written into it, then renamed over the
// target. A directory created for it is private too.
func (f *File) Save(path string) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		if err := makePrivateDir(dir); err != nil {
			return fmt.Errorf("protect %s: %w", dir, err)
		}
	}
	b, err := toml.Marshal(f)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := makePrivateFile(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect config: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// ProfileNames returns the profile names, sorted.
func (f *File) ProfileNames() []string {
	names := make([]string, 0, len(f.Profiles))
	for n := range f.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Settings is the effective connection configuration.
type Settings struct {
	Profile           string
	URL               string
	Key               string
	DefaultVisibility api.Visibility
}

// Resolve applies the precedence rules: flags, then environment
// (VELA_PROFILE, VELA_URL, VELA_KEY), then the config file. There is no flag
// for the key on purpose: command lines end up in shell history and in
// process listings visible to other users.
func Resolve(f *File, flagProfile, flagURL string, lookupEnv func(string) (string, bool)) (Settings, error) {
	env := func(k string) string {
		v, _ := lookupEnv(k)
		return strings.TrimSpace(v)
	}
	s := Settings{Profile: flagProfile}
	explicit := s.Profile != ""
	if s.Profile == "" {
		s.Profile = env("VELA_PROFILE")
		explicit = s.Profile != ""
	}
	if s.Profile == "" {
		s.Profile = f.DefaultProfile
	}
	if s.Profile == "" && len(f.Profiles) == 1 {
		s.Profile = f.ProfileNames()[0]
	}
	if s.Profile == "" {
		s.Profile = DefaultProfileName
	}
	p, ok := f.Profiles[s.Profile]
	if !ok && explicit {
		return s, fmt.Errorf("no profile %q in the config (have: %s)", s.Profile, strings.Join(f.ProfileNames(), ", "))
	}
	if p == nil {
		p = &Profile{}
	}
	s.URL = firstNonEmpty(flagURL, env("VELA_URL"), p.URL)
	s.Key = firstNonEmpty(env("VELA_KEY"), p.Key)
	if p.DefaultVisibility != "" {
		v, err := api.ParseVisibility(p.DefaultVisibility)
		if err != nil {
			return s, fmt.Errorf("profile %q: default_visibility: %w", s.Profile, err)
		}
		s.DefaultVisibility = v
	}
	return s, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
