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
| `Path` | `string` | Current root-relative directory path, including trailing `/` |
| `Breadcrumbs` | `[]Crumb` | Root through current directory |
| `Dirs` | `[]Dir` | Child directories containing public, unexpired objects |
| `Files` | `[]File` | Public, unexpired files in this directory |
| `Parent` | `string` | Root-relative parent URL; empty at root |
| `GeneratedAt` | `time.Time` | UTC render time |
| `Version` | `string` | Server build version |

Nested fields:

- `Site`: `Title`, `URL` (strings).
- `Crumb`: `Name`, `URL` (strings).
- `Dir`: `Name`, `URL` (strings), `Count` (int, public objects anywhere below
  the directory), `UpdatedAt` (UTC `time.Time`).
- `File`: `Name`, `URL`, `Title`, `Description`, `HumanSize`, `ContentType`,
  `Kind` (strings); `Tags` (`[]string`), `Size` (int64 bytes), `UpdatedAt`
  (UTC `time.Time`).

Directory and file URLs are escaped per segment and root-relative. Rows sort
naturally, case-insensitively, with numeric runs compared by value. `Kind` selects
styling/icons: image, video, audio, pdf, archive, code, text, document, spreadsheet,
presentation, font, or file. Templates do not receive private/unlisted objects,
API keys, signatures, or blob identifiers.

Error pages receive `ErrorPage`: `Site`, `Status` (int), `Title`, `Message`
(strings), `GeneratedAt` (UTC `time.Time`), and `Version` (string). The shared
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
