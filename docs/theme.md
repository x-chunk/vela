# Public index theme contract

[`internal/index`](../internal/index/index.go) embeds the default theme and loads
optional overrides from `VELA_THEME_DIR`. A partial override falls back to the
embedded version of each missing file. Assets with new names can also be added.
Files are loaded and templates parsed and sample-rendered before startup;
invalid templates or missing assets fail startup. Changes require a restart.

```
theme/
├── layout.html
├── index.html
├── error.html
└── assets/
    ├── style.css
    ├── favicon.svg
    └── favicon.ico
```

Mount overrides read-only at a container path and set `VELA_THEME_DIR` to that
path. `layout.html` defines `layout`; `index.html` and `error.html` each define
`content`. They are parsed separately with the shared layout. The embedded
layout also offers a `title` block. Templates use Go `html/template` escaping;
object names and metadata remain untrusted text.

## Template data

The authoritative types are in [`internal/index/page.go`](../internal/index/page.go).
Field names are an extension contract: renaming/removing fields breaks custom
themes. HTML and directory `?format=json` use the same public `Page` data.
JSON fields use the struct's snake_case tags; Go templates use exported names.

| Page field | Type | Meaning |
| --- | --- | --- |
| `Site` | `Site` | Configured title and public origin |
| `Title`, `Description` | `string` | Own policy metadata; title falls back to directory name or site title |
| `NoIndex` | `bool` | Layout must emit the robots meta tag when true |
| `RootDisabled` | `bool` | Suppress navigation links to a disabled root |
| `PolicyCounts` | `bool` | Use visible-entry labels for counts when rules exist |
| `CustomTitle` | `bool` | Distinguishes an explicit title from the fallback |
| `Path` | `string` | Current root-relative directory path, including trailing `/` |
| `Breadcrumbs` | `[]Crumb` | Root through current directory |
| `Dirs` | `[]Dir` | Visible child directories with enabled, nonempty indexes |
| `Files` | `[]File` | Public, unexpired files in this directory |
| `Parent` | `string` | Root-relative parent URL; empty at root or when the parent index is disabled |
| `GeneratedAt` | `time.Time` | UTC render time |
| `Version` | `string` | Server build version |

Nested fields:

- `Site`: `Title`, `URL` (strings).
- `Crumb`: `Name`, `URL` (strings). An empty URL must render text, not a link.
- `Dir`: `Name`, `URL`, `Title`, `Description` (strings), `Count` (int, visible
  files plus child directories on its page; legacy subtree file count without
  rules), `UpdatedAt` (latest reachable visible object, UTC `time.Time`).
- `File`: `Name`, `URL`, `Title`, `Description`, `HumanSize`, `ContentType`,
  `Kind` (strings); `Tags` (`[]string`), `Size` (int64 bytes), `UpdatedAt`
  (UTC `time.Time`).

Directory and file URLs are escaped per segment and root-relative. Rows sort
naturally, case-insensitively, with numeric runs compared by value. `Kind` selects
styling/icons: image, video, audio, pdf, archive, code, text, document, spreadsheet,
presentation, font, or file. Templates do not receive private/unlisted objects,
API keys, signatures, or blob identifiers.

Error pages receive `ErrorPage`: `Site`, `Status` (int), `Title`, `Message`
(strings), `GeneratedAt` (UTC `time.Time`), and `Version` (string). `NoIndex` and `RootDisabled` are also available to the
shared layout and default false on error pages. The shared
layout must work with both Page and ErrorPage; page-specific fields belong in
its individual content/title templates.

## Helpers and browser constraints

| Helper | Result |
| --- | --- |
| `asset "style.css"` | `/_vela/assets/` URL with a 12-character content-hash query; unknown assets fail |
| `icon "pdf"` | Trusted built-in SVG for a kind or navigation icon |
| `date .UpdatedAt` | UTC `YYYY-MM-DD HH:MM`; empty for zero time |
| `iso .UpdatedAt` | UTC RFC 3339 string |

Use external theme CSS and `asset` URLs for cache invalidation. Versioned asset
requests may be cached for a year; unversioned requests use a shorter lifetime.
The index/error Content-Security-Policy restricts default resources and styles
to the same origin and permits data images. It blocks inline scripts/styles,
forms, base URL overrides, and framing; same-origin script files are allowed
by the default resource policy.
Customization does not relax this policy; uploaded object content follows its
own delivery rules. `/favicon.ico` serves the corresponding theme asset.

Reference templates live in [`internal/index/theme/`](../internal/index/theme/).
Rendering, escaping, partial overrides, and startup checks are covered by
[`index_test.go`](../internal/index/index_test.go).

## Directory policy compatibility

**Breaking for custom themes:** breadcrumb URLs can now be empty. Render the
name as plain text in that case, and suppress `Parent` when empty. `Parent`
remains the existing string; do not assume a `Parent.URL` object. When
`NoIndex` is true, `layout.html` must include:

```html
<meta name="robots" content="noindex, nofollow">
```

`Title` and `Description` are plain text escaped by html/template. Directory
rows also expose their own title/description. Keep raw directory names for
navigation, and respect `RootDisabled` for brand/root links. The default theme
preserves legacy rendering byte-for-byte when no policies are stored.
