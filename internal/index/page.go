package index

import (
	"mime"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/domain"
	"go.xchunk.org/vela/internal/units"
)

// Visibility indicators of signed-in pages (docs/SPEC-web.md). Anonymous
// pages never carry flags.
const (
	FlagPrivate  = "private"
	FlagUnlisted = "unlisted"
	FlagHidden   = "hidden"
	FlagNoIndex  = "no-index"
	// FlagSymlink marks a file that serves the content of another file.
	FlagSymlink = "symlink"
)

// Session describes the signed-in web session a page is rendered for. Pages
// for anonymous visitors have none.
type Session struct {
	// Prefix is the directory prefix of the session's key; empty with Full.
	Prefix string `json:"prefix"`
	// Full is set when the key has no prefix and sees the whole store.
	Full bool `json:"full"`
	// SignOutURL is the action of the sign-out form.
	SignOutURL string `json:"-"`
}

// Site describes the whole store.
type Site struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Crumb is one breadcrumb: the root and every directory up to the current one.
type Crumb struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Dir is a sub-directory row.
type Dir struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Count is the number of visible entries; without rules it preserves the legacy subtree file count.
	Count     int       `json:"count"`
	UpdatedAt time.Time `json:"updated_at"`
	// Flags lists the visibility indicators of a signed-in page: hidden,
	// no-index. Always empty for anonymous visitors.
	Flags []string `json:"flags,omitempty"`
}

// File is a file row.
type File struct {
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Tags        []string  `json:"tags"`
	Size        int64     `json:"size"`
	HumanSize   string    `json:"human_size"`
	ContentType string    `json:"content_type"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Kind is a coarse file category for icons and styling: folder, image,
	// video, audio, pdf, archive, code, text, document, spreadsheet,
	// presentation, font or file.
	Kind string `json:"kind"`
	// Flags lists the visibility indicators of a signed-in page: private,
	// unlisted. Always empty for anonymous visitors.
	Flags []string `json:"flags,omitempty"`
}

// Page is the data of an index page (and of ?format=json).
type Page struct {
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	NoIndex      bool      `json:"noindex"`
	RootDisabled bool      `json:"-"`
	PolicyCounts bool      `json:"-"`
	CustomTitle  bool      `json:"-"`
	Site         Site      `json:"site"`
	Path         string    `json:"path"`
	Breadcrumbs  []Crumb   `json:"breadcrumbs"`
	Dirs         []Dir     `json:"dirs"`
	Files        []File    `json:"files"`
	Parent       string    `json:"parent"`
	GeneratedAt  time.Time `json:"generated_at"`
	Version      string    `json:"version"`
	// Session is the signed-in session the page is rendered for; nil for
	// anonymous visitors, whose JSON therefore stays unchanged.
	Session *Session `json:"session,omitempty"`
	// SignInURL links to the sign-in page when web sign-in is enabled and the
	// visitor is anonymous; empty otherwise.
	SignInURL string `json:"-"`
}

// ErrorPage is the data of an error page.
type ErrorPage struct {
	NoIndex      bool      `json:"-"`
	RootDisabled bool      `json:"-"`
	Site         Site      `json:"site"`
	Status       int       `json:"status"`
	Title        string    `json:"title"`
	Message      string    `json:"message"`
	GeneratedAt  time.Time `json:"generated_at"`
	Version      string    `json:"version"`
	Session      *Session  `json:"-"`
	SignInURL    string    `json:"-"`
}

// LoginPage is the data of the sign-in page. It offers every field that Page
// and ErrorPage have in common, so a custom layout written for those two keeps
// working with the sign-in page.
type LoginPage struct {
	NoIndex      bool
	RootDisabled bool
	Site         Site
	// Title is the page's heading, "Sign in".
	Title string
	// Action is the target of the form; it carries the page to return to.
	Action string
	// Error explains a failed attempt; empty on first display.
	Error       string
	GeneratedAt time.Time
	Version     string
	Session     *Session
	SignInURL   string
}

// DirEntry is the input for one directory row.
type DirEntry struct {
	Title       string
	Description string
	Path        string
	Count       int
	UpdatedAt   time.Time
	// Hidden and IndexOff are the directory's policy for anonymous visitors;
	// they become flags on signed-in pages.
	Hidden   bool
	IndexOff bool
}

// BuildPage assembles the template data for a directory. URLs are
// root-relative so that pages work under any host name. A non-nil session
// marks the page as signed in and adds visibility flags to its rows.
func BuildPage(site Site, version, prefix string, dirs []DirEntry, files []domain.Object, now time.Time, session *Session) Page {
	p := Page{
		Site:        site,
		Path:        "/" + prefix,
		Breadcrumbs: breadcrumbs(site, prefix),
		Dirs:        make([]Dir, 0, len(dirs)),
		Files:       make([]File, 0, len(files)),
		GeneratedAt: now.UTC(),
		Version:     version,
		Session:     session,
	}
	p.Title = site.Title
	if prefix != "" {
		p.Title = api.BaseName(prefix)
		p.Parent = "/" + api.EscapePath(api.ParentPrefix(prefix))
	}
	for _, d := range dirs {
		row := Dir{
			Title: d.Title, Description: d.Description,
			Name:      api.BaseName(d.Path),
			URL:       "/" + api.EscapePath(d.Path),
			Count:     d.Count,
			UpdatedAt: d.UpdatedAt.UTC(),
		}
		if session != nil {
			row.Flags = dirFlags(d)
		}
		p.Dirs = append(p.Dirs, row)
	}
	for _, o := range files {
		tags := o.Tags
		if tags == nil {
			tags = []string{}
		}
		p.Files = append(p.Files, File{
			Name:        api.BaseName(o.Path),
			URL:         "/" + api.EscapePath(o.Path),
			Title:       o.Title,
			Description: o.Description,
			Tags:        tags,
			Size:        o.Size,
			HumanSize:   units.FormatSize(o.Size),
			ContentType: o.ContentType,
			UpdatedAt:   o.UpdatedAt.UTC(),
			Kind:        KindOf(o.Path, o.ContentType),
			Flags:       fileFlags(session, o),
		})
	}
	sort.SliceStable(p.Dirs, func(i, j int) bool { return NaturalLess(p.Dirs[i].Name, p.Dirs[j].Name) })
	sort.SliceStable(p.Files, func(i, j int) bool { return NaturalLess(p.Files[i].Name, p.Files[j].Name) })
	return p
}

func dirFlags(d DirEntry) []string {
	var flags []string
	if d.Hidden {
		flags = append(flags, FlagHidden)
	}
	if d.IndexOff {
		flags = append(flags, FlagNoIndex)
	}
	return flags
}

// fileFlags marks, for signed-in pages only, symlinks and files that
// anonymous visitors cannot find in the index. Anonymous visitors see a
// symlink as a plain file: how content is organized is not theirs to know.
// Public regular files get no indicator, which keeps the list quiet.
func fileFlags(session *Session, o domain.Object) []string {
	if session == nil {
		return nil
	}
	var flags []string
	if o.IsSymlink() {
		flags = append(flags, FlagSymlink)
	}
	switch o.EffectiveVisibility() {
	case api.Private:
		flags = append(flags, FlagPrivate)
	case api.Unlisted:
		flags = append(flags, FlagUnlisted)
	}
	return flags
}

func breadcrumbs(site Site, prefix string) []Crumb {
	crumbs := []Crumb{{Name: site.Title, URL: "/"}}
	acc := ""
	for _, seg := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if seg == "" {
			continue
		}
		acc += seg + "/"
		crumbs = append(crumbs, Crumb{Name: seg, URL: "/" + api.EscapePath(acc)})
	}
	return crumbs
}

// NaturalLess orders names the way people expect: case-insensitively, with
// digit runs compared by numeric value, so "lesson2" sorts before
// "lesson10". Ties fall back to byte order to keep the order total.
func NaturalLess(a, b string) bool {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		ca, cb := a[ai], b[bi]
		if isDigit(ca) && isDigit(cb) {
			as, ae := digitRun(a, ai)
			bs, be := digitRun(b, bi)
			na, nb := strings.TrimLeft(a[as:ae], "0"), strings.TrimLeft(b[bs:be], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			ai, bi = ae, be
			continue
		}
		ra, sa := utf8.DecodeRuneInString(a[ai:])
		rb, sb := utf8.DecodeRuneInString(b[bi:])
		la, lb := unicode.ToLower(ra), unicode.ToLower(rb)
		if la != lb {
			return la < lb
		}
		ai += sa
		bi += sb
	}
	if len(a)-ai != len(b)-bi {
		return len(a)-ai < len(b)-bi
	}
	return a < b
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func digitRun(s string, i int) (int, int) {
	j := i
	for j < len(s) && isDigit(s[j]) {
		j++
	}
	return i, j
}

// KindOf classifies a file for its icon.
func KindOf(name, contentType string) string {
	mt, _, _ := mime.ParseMediaType(contentType)
	switch ext := strings.ToLower(path.Ext(name)); {
	case mt == "application/pdf":
		return "pdf"
	case strings.HasPrefix(mt, "image/"):
		return "image"
	case strings.HasPrefix(mt, "video/"):
		return "video"
	case strings.HasPrefix(mt, "audio/"):
		return "audio"
	case strings.HasPrefix(mt, "font/"):
		return "font"
	case archiveTypes[mt] || archiveExts[ext]:
		return "archive"
	case strings.Contains(mt, "spreadsheet") || mt == "application/vnd.ms-excel" || mt == "text/csv":
		return "spreadsheet"
	case strings.Contains(mt, "presentation") || mt == "application/vnd.ms-powerpoint":
		return "presentation"
	case strings.Contains(mt, "wordprocessing") || strings.Contains(mt, "opendocument.text") ||
		mt == "application/msword" || mt == "application/rtf" || mt == "application/epub+zip":
		return "document"
	case codeExts[ext] || mt == "application/json" || mt == "application/xml" || mt == "text/html" ||
		mt == "text/css" || mt == "text/javascript":
		return "code"
	case strings.HasPrefix(mt, "text/"):
		return "text"
	default:
		return "file"
	}
}

var archiveTypes = map[string]bool{
	"application/zip": true, "application/gzip": true, "application/x-tar": true, "application/x-7z-compressed": true,
	"application/vnd.rar": true, "application/x-xz": true, "application/zstd": true, "application/x-bzip2": true,
}

var archiveExts = map[string]bool{".zip": true, ".gz": true, ".tgz": true, ".tar": true, ".7z": true, ".rar": true, ".xz": true, ".zst": true}

var codeExts = map[string]bool{
	".go": true, ".py": true, ".js": true, ".ts": true, ".rs": true, ".c": true, ".h": true, ".sh": true,
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".xml": true, ".html": true, ".css": true, ".sql": true,
}

func samplePage() Page {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return BuildPage(Site{Title: "sample", URL: "https://example.org"}, "test", "a/",
		[]DirEntry{{Path: "a/b/", Count: 1, UpdatedAt: now}},
		[]domain.Object{{Path: "a/c.pdf", ContentType: "application/pdf", Tags: []string{"t"}, UpdatedAt: now}}, now, nil)
}

// sampleSessionPage exercises the signed-in branches of a theme.
func sampleSessionPage() Page {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return BuildPage(Site{Title: "sample", URL: "https://example.org"}, "test", "a/",
		[]DirEntry{{Path: "a/b/", Count: 1, UpdatedAt: now, Hidden: true, IndexOff: true}},
		[]domain.Object{
			{Path: "a/c.pdf", ContentType: "application/pdf", Visibility: api.Private, UpdatedAt: now},
			{Path: "a/d.txt", ContentType: "text/plain", Visibility: api.Unlisted, UpdatedAt: now},
		}, now, &Session{Prefix: "a/", SignOutURL: "/_vela/logout"})
}

// PolicyResolver keeps theme data independent of persistence and service wiring.
type PolicyResolver interface {
	Effective(prefix string) api.EffectiveDirPolicy
}

// ApplyPolicies removes links to disabled ancestors and adds page metadata.
func (p *Page) ApplyPolicies(view PolicyResolver) {
	prefix := strings.TrimPrefix(p.Path, "/")
	if rules, ok := view.(interface{ HasRules() bool }); ok {
		p.PolicyCounts = rules.HasRules()
	}
	policy := view.Effective(prefix)
	p.NoIndex = policy.NoIndex == api.IndexOn
	p.Description = policy.Description
	if policy.Title != "" {
		p.Title = policy.Title
		p.CustomTitle = true
	}
	p.RootDisabled = view.Effective("").Index == api.IndexOff
	at := ""
	for i := range p.Breadcrumbs {
		if i > 0 {
			at += p.Breadcrumbs[i].Name + "/"
		}
		if view.Effective(at).Index == api.IndexOff {
			p.Breadcrumbs[i].URL = ""
		}
	}
	if prefix != "" && view.Effective(api.ParentPrefix(prefix)).Index == api.IndexOff {
		p.Parent = ""
	}
}
