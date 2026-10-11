// Package index renders the public HTML index and error pages.
//
// The default theme is embedded in the binary. Setting VELA_THEME_DIR
// overrides any of its files by name (layout.html, index.html, error.html,
// assets/...); everything not overridden falls back to the embedded file.
// Templates are parsed once at startup, so a broken custom theme stops the
// server from starting instead of breaking pages at request time.
//
// The data passed to the templates (Page, ErrorPage and their parts) is
// the theme contract documented in docs/theme.md. Renaming or removing a field
// breaks custom themes.
package index

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

//go:embed theme
var embedded embed.FS

// Asset is a static theme file.
type Asset struct {
	Name    string
	Content []byte
	// Hash is a short content hash used as a cache-busting query parameter.
	Hash    string
	ModTime time.Time
}

// Theme holds parsed templates and assets.
type Theme struct {
	indexTmpl *template.Template
	errorTmpl *template.Template
	assets    map[string]*Asset
}

// Load parses the embedded theme, overridden by files from dir if dir is not
// empty.
func Load(dir string) (*Theme, error) {
	base, err := fs.Sub(embedded, "theme")
	if err != nil {
		return nil, err
	}
	var custom fs.FS
	if dir != "" {
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("theme directory: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("theme directory %s is not a directory", dir)
		}
		custom = os.DirFS(dir)
	}
	read := func(name string) ([]byte, error) {
		if custom != nil {
			b, err := fs.ReadFile(custom, name)
			if err == nil {
				return b, nil
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("theme file %s: %w", filepath.Join(dir, name), err)
			}
		}
		return fs.ReadFile(base, name)
	}

	t := &Theme{assets: map[string]*Asset{}}
	if err := t.loadAssets(base, custom); err != nil {
		return nil, err
	}
	layout, err := read("layout.html")
	if err != nil {
		return nil, err
	}
	parse := func(page string) (*template.Template, error) {
		body, err := read(page)
		if err != nil {
			return nil, err
		}
		tmpl := template.New("layout").Funcs(t.funcs())
		if _, err := tmpl.Parse(string(layout)); err != nil {
			return nil, fmt.Errorf("parse layout.html: %w", err)
		}
		if _, err := tmpl.New(page).Parse(string(body)); err != nil {
			return nil, fmt.Errorf("parse %s: %w", page, err)
		}
		if tmpl.Lookup("content") == nil {
			return nil, fmt.Errorf("%s must define a \"content\" template", page)
		}
		return tmpl, nil
	}
	if t.indexTmpl, err = parse("index.html"); err != nil {
		return nil, err
	}
	if t.errorTmpl, err = parse("error.html"); err != nil {
		return nil, err
	}
	// Render both pages once with sample data so that runtime errors in a
	// custom theme (a misspelled field, for example) surface at startup.
	if err := t.RenderIndex(io.Discard, samplePage()); err != nil {
		return nil, fmt.Errorf("theme check: %w", err)
	}
	if err := t.RenderError(io.Discard, ErrorPage{Site: Site{Title: "x"}, Status: 404, Title: "Not found"}); err != nil {
		return nil, fmt.Errorf("theme check: %w", err)
	}
	return t, nil
}

func (t *Theme) loadAssets(base, custom fs.FS) error {
	add := func(fsys fs.FS) error {
		return fs.WalkDir(fsys, "assets", func(p string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && p == "assets" {
				return fs.SkipDir
			}
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			name := strings.TrimPrefix(p, "assets/")
			mod := info.ModTime()
			if mod.IsZero() {
				mod = startTime // embedded files carry no modification time
			}
			t.assets[name] = &Asset{Name: name, Content: b, Hash: hex.EncodeToString(sum[:])[:12], ModTime: mod}
			return nil
		})
	}
	if err := add(base); err != nil {
		return fmt.Errorf("load embedded assets: %w", err)
	}
	if custom != nil {
		if err := add(custom); err != nil {
			return fmt.Errorf("load theme assets: %w", err)
		}
	}
	return nil
}

var startTime = time.Now().UTC().Truncate(time.Second)

// Asset returns a theme asset by name (path below assets/).
func (t *Theme) Asset(name string) (*Asset, bool) {
	a, ok := t.assets[path.Clean(name)]
	return a, ok
}

func (t *Theme) funcs() template.FuncMap {
	return template.FuncMap{
		// asset returns the URL of an asset with a content hash, so assets
		// can be cached for a year and still change with a new theme.
		"asset": func(name string) (string, error) {
			a, ok := t.assets[name]
			if !ok {
				return "", fmt.Errorf("unknown asset %q", name)
			}
			return "/_vela/assets/" + name + "?v=" + a.Hash, nil
		},
		"icon": func(kind string) template.HTML { return iconSVG(kind) },
		"date": func(ts time.Time) string {
			if ts.IsZero() {
				return ""
			}
			return ts.UTC().Format("2006-01-02 15:04")
		},
		"iso": func(ts time.Time) string { return ts.UTC().Format(time.RFC3339) },
	}
}

// RenderIndex renders an index page.
func (t *Theme) RenderIndex(w io.Writer, p Page) error {
	return render(w, t.indexTmpl, p)
}

// RenderError renders an error page.
func (t *Theme) RenderError(w io.Writer, p ErrorPage) error {
	return render(w, t.errorTmpl, p)
}

// render executes into a buffer first, so a template error never leaves a
// half-written page with a 200 status on the wire.
func render(w io.Writer, tmpl *template.Template, data any) error {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		return err
	}
	_, err := buf.WriteTo(w)
	return err
}
