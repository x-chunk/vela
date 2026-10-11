# Configuration reference

[`internal/config/config.go`](../internal/config/config.go) and
[`storage.go`](../internal/config/storage.go) define defaults and validation.
Core settings accept `velad serve` flags: explicit flag → environment → default.
An environment variable set to an empty value counts as set. Administrative
commands accept the common data-directory and logging flags. Backend settings
and credentials are environment-only; use `velad serve --help` for core flags.

## Server settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `VELA_LISTEN` | `:8080` | HTTP listen address |
| `VELA_DATA_DIR` | `/data` | SQLite and filesystem data directory |
| `VELA_PUBLIC_URL` | required to serve | Bare HTTP(S) public origin, no path, query, or credentials |
| `VELA_ACCEL_PREFIX` | empty | Direct delivery; a nonempty prefix explicitly enables nginx accel |
| `VELA_TRUSTED_PROXIES` | `127.0.0.1/32,::1/128` | Peers allowed to supply forwarded IPs and request IDs |
| `VELA_MAX_UPLOAD` | `2GiB` | Streaming upload limit; align proxy body limits |
| `VELA_MAX_CREATE` | `1MiB` | Inline creation limit |
| `VELA_LINK_DEFAULT_TTL` | `24h` | Default signed-link lifetime |
| `VELA_LINK_MAX_TTL` | `30d` | Maximum signed-link lifetime |
| `VELA_LINK_RETENTION` | `7d` | Retention of expired/revoked link records |
| `VELA_TRASH_RETENTION` | `14d` | Retention of deleted objects |
| `VELA_JANITOR_INTERVAL` | `24h` | Janitor interval; at least one minute |
| `VELA_SITE_TITLE` | `Vela` | Public index title |
| `VELA_THEME_DIR` | empty | Optional [theme overrides](theme.md) |
| `VELA_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `VELA_LOG_FORMAT` | `json` | `json` or `text` |
| `VELA_DEFAULT_VISIBILITY` | `unlisted` | `public`, `unlisted`, or `private` |
| `VELA_ROBOTS` | `deny` | `deny` or `allow` for robots.txt |

Size values accept byte counts and units such as MiB/GiB. Core durations use
[`api.ParseDuration`](../api/duration.go), including day durations. Link defaults
cannot exceed the maximum; retention settings must be positive. Configuration
and theme checks run before serving traffic. Startup logs identify delivery mode.

## Storage settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `VELA_STORAGE` | `fs` | `fs` or `r2` (also used for compatible S3 endpoints) |
| `VELA_MAX_CONCURRENT_UPLOADS` | `4` | Active uploads, 1–1024; excess uploads wait with cancellation |
| `VELA_R2_ACCOUNT_ID` | empty | Cloudflare account; needed unless endpoint is explicit |
| `VELA_R2_ENDPOINT` | derived from account | Bare S3 origin; default `https://<account>.r2.cloudflarestorage.com` |
| `VELA_R2_REGION` | `auto` | Request signing region; configure for another S3 service |
| `VELA_R2_BUCKET` | required for R2 | Private bucket |
| `VELA_R2_PREFIX` | `blobs/` | Relative managed prefix ending in `/` |
| `VELA_R2_ACCESS_KEY_ID` | required for R2 | Bucket-scoped access key |
| `VELA_R2_SECRET_ACCESS_KEY` | required for R2 | Bucket-scoped secret |
| `VELA_R2_DELIVERY` | `proxy` | `proxy` or `redirect` |
| `VELA_R2_PRESIGN_TTL` | `5m` | Redirect grant duration, 1 second–1 hour |
| `VELA_R2_PART_SIZE` | `8MiB` | Multipart part size, 5–64 MiB |
| `VELA_R2_UPLOAD_CONCURRENCY` | `2` | Concurrent parts per upload, 1–16 |
| `VELA_R2_PATH_STYLE` | `true` | Path-style rather than virtual-hosted bucket addressing |

Credentials are explicit; the backend does not read `AWS_*`, `~/.aws`, or
instance metadata. R2 serving requires an empty accel prefix and upload/create
limits within fixed part size × 10,000. R2 presign durations use Go duration
syntax. See [storage capacity and delivery](storage.md) before tuning buffers.
Scenario env files in [`examples/env/`](../examples/env/) show all applicable
settings; keep populated credential files outside version control.

## CLI connection state

The CLI resolves `--url` and `--profile`, then `VELA_URL` and `VELA_PROFILE`, then
saved profiles. `VELA_KEY` overrides the saved key; there is deliberately no key
flag. `vela login` has no built-in server URL: a flag, environment value, saved
profile URL, or interactive answer is needed. It verifies the key before saving.

Profiles are TOML at the platform config directory plus `vela/config.toml`
(`$XDG_CONFIG_HOME/vela/config.toml` when set on Unix). `--config` or `VELA_CONFIG`
overrides the path. Each profile stores URL, key, and optional default visibility.
Writes are atomic. Unix files/directories are 0600/0700; Windows uses protected
DACLs for the current user and SYSTEM. Loading an exposed file fails. Definitions
and precedence live in [`internal/cli/config`](../internal/cli/config/config.go).

## Diagnostic boundaries

| Symptom | Check |
| --- | --- |
| API 401 | Bearer key validity/revocation and selected CLI profile |
| API 403 | Required scope and key prefix, including move/restore target |
| Public 404 | Visibility, expiry, trash, and path; private objects intentionally hide existence |
| Empty download or proxy 404 in accel | Prefix matches both internal nginx locations and readable blob mount |
| Wrong client IP or shared auth throttling | Actual proxy peer is explicitly trusted; defaults assume no Docker subnet |
| Data directory permission failure | Container UID/GID 65532 owns the writable mount |
| CLI/server incompatibility | `/api/v1/info` reports the same API major version as the CLI |

Proxy and TLS assumptions are recorded with the [deployment examples](../examples/README.md).
