# Changelog

## Unreleased

## v0.10.0 — 2026-10-31

### Added

- `vela ln -r` takes `--skip`, `--skip-explicitly` and `--skip-symlinks`, as
  with `--by-tag`: conflicting destinations are left out instead of failing
  the whole tree, and symlinks in the source are not linked. Needs a server
  that announces `features.symlink_tree_skip`.
- `vela ln -r … --sync` links only what is missing in DEST, like
  `ln --by-tag … --sync`, and takes `--prune`, `--prune-trashed`, `--strict`,
  `--copy-meta` and `-y`. Symlinks to files outside SOURCE are never pruned.

### Changed

- `vela ln -r DIR/ DIR/sub/` links a directory into its own subdirectory
  instead of refusing it; the destination is left out of the walk, so a
  rerun does not nest.
- Tag-only `ln` flags given without `--by-tag` are named one by one in the
  usage error.

## v0.9.0 — 2026-10-31

### Added

- `vela ln --by-tag … --sync --copy-meta` copies changed metadata of the
  files to their existing symlinks in place (URLs and signed links keep
  working); `--copy-meta=title,tags` limits it to these fields.

### Fixed

- Conflicts of a refused `mv --by-tag`, `ln --by-tag` or
  `trash restore --by-tag` are listed on separate lines instead of one line
  joined by a literal `\n`.

## v0.8.0 — 2026-10-28

### Added

- `vela ln --by-tag … --sync` links only what is missing in DEST, so it can
  be rerun to keep a tag view current; `--prune` also moves to the trash
  the symlinks whose file in scope lost its tags or is in the trash. A new
  symlink may take a pruned one's place (with a warning); `--strict` makes
  that a conflict.
  Symlinks to trashed files are kept unless `--prune-trashed` is given,
  since restoring the file repairs them; symlinks to tagged files moved out
  of scope are pruned.

## v0.7.0 — 2026-10-28

### Added

- `vela ln --by-tag` and `--by-tags` link a tag selection into a directory,
  flat or with `--save-structure`. Conflicts stop it before the first
  symlink; `--skip` leaves conflicting objects out silently and
  `--skip-explicitly` reports each one. `--skip-symlinks` leaves symlinks,
  which copy their file's tags, out of the selection. Like `mv`, it shows
  the plan and asks first (`-y`, `--dry-run`).

## v0.6.0 — 2026-10-28

### Added

- Tag selection: `--by-tag x` (one tag; repeated flags are alternatives) and
  `--by-tags "a b"` (all of the tags) select objects instead of paths in
  `ls`, `stat`, `url`, `open`, `get`, `meta`, `vis`, `share`, `links ls`,
  `links revoke`, `rm`, `mv`, `trash ls`, `trash purge` and `trash restore`.
  Paths narrow the search. `rm`, `mv`, `share`, `open`, `links revoke` and
  the trash commands show the plan and ask before acting (`-y` skips the
  question, `--dry-run` only shows it). `get` keeps the directory structure;
  `mv` moves flat or with `--save-structure` and reports every conflict
  before the first move.
- `GET /api/v1/objects` and `GET /api/v1/trash` accept a repeated `tag`
  parameter (comma-separated groups, OR-ed); the Go client also filters the
  results, so older servers cannot widen a selection.

## v0.5.0 — 2026-10-28

### Added

- `vela meta -r` changes tags, description, disposition and expiry of several
  files and everything below directories. Tags are edited per object, objects
  that already match are left alone, and `--title` and `--content-type` are
  refused with `-r`.

## v0.4.0 — 2026-10-28

### Added

- Symlinks: `vela ln` (and `POST /api/v1/symlinks`) creates entries that serve
  another file's content, referring to it by ID, so renames keep them working
  and deletions leave them broken. Anonymous visitors get the stricter of the
  two visibilities. Listings gain `kind`, `broken` and `target` filters
  (`vela ls --type/--broken/--target`), deletes report `symlinks_affected`, and
  `vela rm --broken` cleans up. Signed-in index pages mark symlinks with a new
  `symlink` flag. See [symlinks](docs/SPEC-symlinks.md).

### Upgrading

- `velad serve` applies migration `0006_symlinks` on start, which rebuilds the
  `objects` and `trash` tables; take a backup first, as an older image cannot
  use the migrated database. Writing content to the path of a symlink fails
  with the new error code `is_symlink` (409). No configuration or proxy
  changes are needed.

## v0.3.0 — 2026-10-18

### Added

- Optional read-only web sign-in with an API key (`VELA_WEB_AUTH`, default
  off): the owner can browse everything a key can see, including private,
  unlisted and hidden content, with icons marking what anonymous visitors
  cannot find. Sessions follow their key, expire after 7 days idle or 30 days
  (`VELA_WEB_SESSION_IDLE`, `VELA_WEB_SESSION_MAX`), and can be ended with
  `velad session revoke-all`. See [web sign-in](docs/web-auth.md).
- Theme contract: `Flags`, `Session`, `SignInURL`, the `flagIcon` helper and an
  optional `login.html`. Themes without them keep working.

### Changed

- While web sign-in is enabled, uploads other than images, audio, video, PDF
  and plain text (HTML and SVG included) are served with a
  `Content-Security-Policy` sandbox, public responses carry
  `Vary: Cookie`, and HTML pages `Referrer-Policy: same-origin`. Without it,
  responses are unchanged; the stylesheet's cache-busting hash changes because
  of new styles.

### Upgrading

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
