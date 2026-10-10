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
	Name string `json:"name"`
	URL  string `json:"url"`
	// Count is the number of public objects anywhere below the directory.
	Count     int       `json:"count"`
	UpdatedAt time.Time `json:"updated_at"`
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
}

// Page is the data of an index page (and of ?format=json).
type Page struct {
	Site        Site      `json:"site"`
	Path        string    `json:"path"`
	Breadcrumbs []Crumb   `json:"breadcrumbs"`
	Dirs        []Dir     `json:"dirs"`
	Files       []File    `json:"files"`
	Parent      string    `json:"parent"`
	GeneratedAt time.Time `json:"generated_at"`
	Version     string    `json:"version"`
}

// ErrorPage is the data of an error page.
type ErrorPage struct {
	Site        Site      `json:"site"`
	Status      int       `json:"status"`
	Title       string    `json:"title"`
	Message     string    `json:"message"`
	GeneratedAt time.Time `json:"generated_at"`
	Version     string    `json:"version"`
}

// DirEntry is the input for one directory row.
type DirEntry struct {
	Path      string
	Count     int
	UpdatedAt time.Time
}

// BuildPage assembles the template data for a directory. URLs are
// root-relative so that pages work under any host name.
func BuildPage(site Site, version, prefix string, dirs []DirEntry, files []domain.Object, now time.Time) Page {
	p := Page{
		Site:        site,
		Path:        "/" + prefix,
		Breadcrumbs: breadcrumbs(site, prefix),
		Dirs:        make([]Dir, 0, len(dirs)),
		Files:       make([]File, 0, len(files)),
		GeneratedAt: now.UTC(),
		Version:     version,
	}
	if prefix != "" {
		p.Parent = "/" + api.EscapePath(api.ParentPrefix(prefix))
	}
	for _, d := range dirs {
		p.Dirs = append(p.Dirs, Dir{
			Name:      api.BaseName(d.Path),
			URL:       "/" + api.EscapePath(d.Path),
			Count:     d.Count,
			UpdatedAt: d.UpdatedAt.UTC(),
		})
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
		})
	}
	sort.SliceStable(p.Dirs, func(i, j int) bool { return NaturalLess(p.Dirs[i].Name, p.Dirs[j].Name) })
	sort.SliceStable(p.Files, func(i, j int) bool { return NaturalLess(p.Files[i].Name, p.Files[j].Name) })
	return p
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
		[]domain.Object{{Path: "a/c.pdf", ContentType: "application/pdf", Tags: []string{"t"}, UpdatedAt: now}}, now)
}
