# Changelog

## Unreleased

### Added

- Optional read-only web sign-in with an API key (`VELA_WEB_AUTH`, default
  off): the owner can browse everything a key can see, including private,
  unlisted and hidden content, with icons marking what anonymous visitors
  cannot find. Sessions follow their key, expire after 7 days idle or 30 days
  (`VELA_WEB_SESSION_IDLE`, `VELA_WEB_SESSION_MAX`), and can be ended with
  `velad session revoke-all`. See [web sign-in](docs/web-auth.md).
- Theme contract: `Flags`, `Session`, `SignInURL`, the `flagIcon` helper and an
  optional `login.html`. Themes without them keep working.
- Symlinks: `vela ln` (and `POST /api/v1/symlinks`) creates entries that serve
  another file's content, referring to it by ID, so renames keep them working
  and deletions leave them broken. Anonymous visitors get the stricter of the
  two visibilities. Listings gain `kind`, `broken` and `target` filters
  (`vela ls --type/--broken/--target`), deletes report `symlinks_affected`, and
  `vela rm --broken` cleans up. See [symlinks](docs/SPEC-symlinks.md).

### Changed

- While web sign-in is enabled, uploads other than images, audio, video, PDF
  and plain text (HTML and SVG included) are served with a
  `Content-Security-Policy` sandbox, public responses carry
  `Vary: Cookie`, and HTML pages `Referrer-Policy: same-origin`. Without it,
  responses are unchanged; the stylesheet's cache-busting hash changes because
  of new styles.

### Upgrading

- `velad serve` applies migration `0006_symlinks` on start, which rebuilds the
  `objects` and `trash` tables; take a backup first, as an older image cannot
  use the migrated database. Writing content to the path of a symlink fails
  with the new error code `is_symlink` (409).
- `velad serve` applies migration `0005_web_sessions` on start; take a backup
  first, as an older image cannot use the migrated database.
- Before enabling web sign-in with nginx accel, add the new `Vary` and
  `Content-Security-Policy` lines of `examples/nginx/accel-locations.conf` to
  both internal locations. Behind a CDN, bypass its cache for requests with the
  `__Host-vela_session` cookie.

## v0.2.1 — 2026-10-15

### Fixed

- CLI transfer progress fits the terminal width and redraws in place instead
  of leaving wrapped frames in scrollback. Long Unicode filenames remain valid
  and cannot inject terminal controls; retries reset the speed calculation.

### Changed

- Transfer bars use a blue fill and turn green on completion, respecting
  `--no-color` and `NO_COLOR`. Narrow windows show fewer details. Live progress
  is suppressed with `TERM=dumb` or `--verbose` to keep diagnostics readable.
  No new dependencies are required.

## v0.2.0 — 2026-10-12

### Added

- Independent directory policies with inherited index/crawler settings, hidden
  entrances, titles/descriptions, `/api/v1/dirs`, and `vela dir` commands.
  Hidden directories are not access control; see [directories](docs/directories.md).
- `VELA_ROOT_INDEX` defaults to `on`; an explicit root database rule overrides it.
- Recursive moves atomically carry policies and report `policies_moved`.
- `VELA_HOST_PORT` selects the loopback host port in the Docker run examples
  (default 8080); the proxy upstream must match it.
- Caddy example for fs-direct and r2 with automatic certificates and query-free
  access and error logs; `make validate-examples` checks it.

### Changed

- **Breaking for custom themes:** breadcrumb URLs can be empty for disabled
  ancestors; render those names as text. Honor `Page.NoIndex` in the layout and
  empty `Page.Parent`. Directory rows add title/description and use visible-entry
  counts once a rule exists. Without rules, legacy HTML/counts remain unchanged.
- Directory `vela url` and `vela open` resolve public index availability on
  servers with directory policies and explain disabled or absent indexes.
  Older API v1 servers retain legacy URL/open behavior and long listings
  without policy annotations.

### Upgrading

- `velad serve` applies migration `0004_dir_policies` on start. Take a backup
  first: an older image cannot be used on the migrated database, so restoring
  that backup is the rollback path.
- No proxy changes are required. Update the CLI to use `vela dir`.

## v0.1.0 — 2026-10-11

First public release: REST API v1, `vela` CLI, public index, expiring signed
links, recoverable trash, SQLite metadata, and filesystem or private R2/S3
content storage.

### Added

- MIT license, contribution guide, and security reporting policy.
- MIT license included in the server image and CLI release archives.
- Separate deployment examples for filesystem direct delivery, explicit nginx
  accel, and private R2/S3 storage; configurable image, paths, and container name.
- `VELA_R2_REGION` (default `auto`) for other S3 signing regions. An explicit
  `VELA_R2_ENDPOINT` no longer requires a Cloudflare account ID.
- CI checks for neutral examples, secret leaks, shell scripts, and nginx syntax.
- Docker Hub release images for both linux/amd64 and linux/arm64, with MIT and
  documentation OCI labels.

### Changed (from pre-release builds)

- **Breaking for existing deployments:** `VELA_ACCEL_PREFIX` now defaults to
  empty (Go streams file contents). Existing nginx accel deployments must set
  `VELA_ACCEL_PREFIX=/_blobs/` explicitly and retain matching internal locations.
- `VELA_TRUSTED_PROXIES` now defaults to loopback only. A host proxy reaching a
  Docker bridge container must have its actual gateway address added explicitly.
- `VELA_SITE_TITLE` now defaults to `Vela`. Set it explicitly to keep branding.
- `vela login` has no built-in server URL. Supply `--url`, `VELA_URL`, an existing
  profile URL, or an interactive answer.
- Deployment examples moved from `deploy/` to `examples/` and now describe the
  operator's responsibility for proxy and TLS configuration.
- Examples and design documents use neutral domains, addresses, and scenarios.
- Release publishing uses `vars.DOCKERHUB_IMAGE`, `secrets.DOCKERHUB_USERNAME`,
  and `secrets.DOCKERHUB_TOKEN`. Set the image variable to `xchunk/vela` for the
  official release; use a repository-scoped access token with Read & Write.

### Removed

- Obsolete private-registry instructions and personal deployment defaults.
