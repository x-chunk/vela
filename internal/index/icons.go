package index

import "html/template"

// iconPaths are 24x24 stroke icons drawn for the default theme. They are
// inlined into the page (no extra requests, and allowed by the index CSP,
// which only restricts loaded resources and style attributes).
var iconPaths = map[string]string{
	"logo":         `<path d="M4 5l8 14 8-14"/><path d="M8.5 5h7"/>`,
	"parent":       `<path d="M12 19V5"/><path d="M5 12l7-7 7 7"/>`,
	"folder":       `<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>`,
	"file":         fileBase,
	"text":         fileBase + `<path d="M8 13h8M8 17h6"/>`,
	"document":     fileBase + `<path d="M8 11h8M8 14h8M8 17h5"/>`,
	"pdf":          fileBase + `<path d="M8 13h8M8 17h5"/><path d="M8 9.5h2"/>`,
	"code":         fileBase + `<path d="M10 12.5l-2 2.25 2 2.25M14 12.5l2 2.25-2 2.25"/>`,
	"spreadsheet":  fileBase + `<path d="M8 11h8v7H8zM8 14.5h8M12 11v7"/>`,
	"font":         fileBase + `<path d="M9.5 18l2.5-6.5 2.5 6.5M10.4 16h3.2"/>`,
	"image":        `<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="10" r="1.75"/><path d="M21 16l-5-5-9 9"/>`,
	"video":        `<rect x="3" y="5" width="13" height="14" rx="2"/><path d="M16 10l5-3v10l-5-3z"/>`,
	"audio":        `<path d="M9 18V6l10-2v12"/><circle cx="6.5" cy="18" r="2.5"/><circle cx="16.5" cy="16" r="2.5"/>`,
	"archive":      `<rect x="3" y="4" width="18" height="5" rx="1"/><path d="M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9M10 13h4"/>`,
	"presentation": `<rect x="3" y="4" width="18" height="12" rx="1"/><path d="M12 16v4M8 20h8"/>`,
	// Visibility indicators of signed-in pages.
	"lock":     `<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4M12 15v2"/>`,
	"unlisted": `<path d="M9 8H7a4 4 0 0 0 0 8h2M15 8h2a4 4 0 0 1 0 8h-2M8 12h8"/>`,
	"hidden":   `<path d="M2 12s3.5-6.5 10-6.5S22 12 22 12s-3.5 6.5-10 6.5S2 12 2 12z"/><circle cx="12" cy="12" r="3"/><path d="M4 4l16 16"/>`,
	"no-index": `<path d="M4 6h16M4 11h9M4 16h6"/><circle cx="17" cy="16" r="4"/><path d="M14.2 18.8l5.6-5.6"/>`,
}

// flags maps each visibility flag to its icon and tooltip text.
var flags = map[string]struct{ icon, tip string }{
	FlagPrivate:  {"lock", "Private: needs a key or signed link"},
	FlagUnlisted: {"unlisted", "Unlisted: anyone with the URL can open it"},
	FlagHidden:   {"hidden", "Hidden: not shown to anonymous visitors in the parent listing"},
	FlagNoIndex:  {"no-index", "Index disabled for anonymous visitors"},
}

const fileBase = `<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>`

// iconSVG returns the inline SVG for a kind, falling back to a generic file.
// The markup comes from the constant table above, never from user data, so
// marking it safe for html/template is sound.
func iconSVG(kind string) template.HTML {
	paths, ok := iconPaths[kind]
	if !ok {
		kind, paths = "file", fileBase
	}
	return template.HTML(`<svg class="icon icon-` + kind + `" viewBox="0 0 24 24" width="20" height="20" fill="none" ` + // #nosec G203 -- constant markup
		`stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">` +
		paths + `</svg>`)
}

// flagSVG renders a visibility indicator with its tooltip. The tooltip is
// drawn by style.css from data-tip, because the CSP blocks inline styles and
// scripts; title is left out so the native tooltip does not appear as well.
// tabindex lets keyboard users focus the icon to read it. Unknown flags
// render nothing, so themes keep working when new flags appear. All markup
// comes from the constant tables above, which makes marking it safe sound.
func flagSVG(flag string) template.HTML {
	f, ok := flags[flag]
	if !ok {
		return ""
	}
	return template.HTML(`<span class="flag flag-`+flag+`" role="img" tabindex="0" aria-label="`+f.tip+`" data-tip="`+f.tip+`">`) + // #nosec G203 -- constant markup
		iconSVG(f.icon) + `</span>`
}
