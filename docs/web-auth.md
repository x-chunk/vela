# Web sign-in

Optional, read-only browser sign-in with an API key. The specification is
[SPEC-web.md](SPEC-web.md); its last section records implementation decisions.
Sessions live in [`internal/service/web.go`](../internal/service/web.go), the
signed-in views in [`internal/service/webview.go`](../internal/service/webview.go),
and the routes, cookie and response headers in
[`internal/server/web.go`](../internal/server/web.go).

Set `VELA_WEB_AUTH=on` to enable it (see [configuration](configuration.md)).
While it is off, which is the default, no route, cookie, header or page changes.
Serving with sign-in requires an `https` public URL, because browsers drop the
`Secure` cookie over plain HTTP; `VELA_WEB_INSECURE_COOKIE=on` exists for local
development only.

## Model

A web session is a thin wrapper around an API key, not a new credential. Any
scope can sign in, and the session is always read-only: there is no write
route for it, and its principal carries the `ro` scope whatever the key may
do. The key is read again on every request, so revoking it makes the very next
request anonymous, and the session always uses the key's current prefix.

## Routes

| Method | Path | Result |
| --- | --- | --- |
| GET, HEAD | `/_vela/login?next=/path/` | Sign-in form: one password field `key` and one button |
| POST | `/_vela/login?next=/path/` | `303` to `next` with the session cookie; `401` for any invalid key |
| POST | `/_vela/logout?next=/path/` | Deletes the session row, expires the cookie, `303` to `next` |

The key is accepted only in the form body (`application/x-www-form-urlencoded`,
at most 4 KiB). A key in a URL, such as `?key=` or `?token=`, is ignored, never
echoed and, like every query string, never logged. `next` must be a
root-relative path; `//host`, schemes, backslashes, control characters and the
sign-in routes themselves become `/`, and its query is dropped.

Both POST routes accept only same-origin requests: `Sec-Fetch-Site:
same-origin`, or, for browsers without it, an `Origin` equal to
`VELA_PUBLIC_URL`. Anything else is `403`, which stops cross-site sign-in and
sign-out. Failed sign-ins show one generic message, take the same time for
unknown and wrong keys, and are limited to five per minute per client IP;
then the form answers `429` with `Retry-After`. The client IP comes from
forwarding headers only from `VELA_TRUSTED_PROXIES`. This budget is separate
from the API's failed-authentication limit.

## Sessions

| Property | Value |
| --- | --- |
| ID | 256 bits from `crypto/rand`, base64url in the cookie only |
| Stored | SHA-256 of the ID, key ID, created, last seen, absolute expiry (`web_sessions`) |
| Cookie | `__Host-vela_session`; `Path=/`, `Secure`, `HttpOnly`, `SameSite=Lax`, no `Domain`; `Max-Age` = absolute lifetime |
| Development cookie | `vela_session` without `Secure`, only with `VELA_WEB_INSECURE_COOKIE` |
| Idle timeout | `VELA_WEB_SESSION_IDLE`, default `7d`, sliding |
| Absolute lifetime | `VELA_WEB_SESSION_MAX`, default `30d` |

`SameSite=Lax` keeps links opened from chats and notes signed in; every state
change is a `POST`. Activity is written at most once per hour (every half idle
timeout when that is shorter), so a session can end up to that interval before
the configured idle timeout. Signing in again replaces the browser's previous
session. Unknown, idle, expired and revoked-key sessions all make the request
anonymous and are deleted when seen. The janitor task `web_sessions` purges
the rest; `velad session revoke-all` ends every session at once, for example
after a cookie may have leaked.

## What a session sees

The visible tree is the anonymous view plus the full view of the key's prefix
`K`, matched on whole path segments (`test/` never covers `test2/`).

| Location | Signed-in behavior |
| --- | --- |
| Inside `K` | Every unexpired object is listed and served, whatever its visibility; directory policies are ignored |
| Strict ancestors of `K` | The anonymous listing (none where the anonymous index is disabled) plus one entrance: the child on the way to `K`, even if hidden or below a disabled index |
| Everything else | Exactly anonymous, status codes included: a path the session cannot see is `404`, never `403` |

A key without prefix sees everything, including hidden directories and a
disabled root. The entrance exists while `K` holds at least one live object;
with an empty `K` its ancestors are plain anonymous pages. Breadcrumbs, the
parent link and slashless directory redirects follow the same rules. Expired
and trashed objects stay invisible. Rows count the entries of the page they
lead to as the session sees it; without any stored policy, counts are subtree
file counts as in the anonymous index.

Signed-in pages mark what anonymous visitors cannot find with icons:
`private` and `unlisted` files, `hidden` directories and directories whose
index is disabled (`no-index`). Public files have no indicator. Templates and
`?format=json` receive these `Flags` and a `Session` only for a valid session;
anonymous JSON is unchanged. See the [theme contract](theme.md).

## Caching and proxies

With sign-in enabled, index, direct-URL and themed error responses carry
`Vary: Cookie`; the sign-in page and every response produced for a valid
session (pages, JSON, files, redirects, 404s) carry `Cache-Control: private,
no-store`. Anonymous responses keep their previous caching. In nginx accel mode
velad sends `X-Vela-Vary`, because nginx drops `Vary` from an `X-Accel-Redirect`
response, and the internal locations in
[`accel-locations.conf`](../examples/nginx/accel-locations.conf) re-emit it.
Proxies in front of Vela must not cache these routes.

A CDN may ignore `Vary: Cookie` (Cloudflare caches by file extension). Add a
cache bypass rule for requests that carry the `__Host-vela_session` cookie, or
signed-in responses could be served to anonymous visitors and the reverse.

## Browser policies

| Response | Policy |
| --- | --- |
| Anonymous index and error pages | Unchanged CSP with `form-action 'none'`; the footer offers a plain "sign in" link |
| Sign-in page and pages for a session | Same CSP with only `form-action 'self'`; nothing else is relaxed |
| Every HTML page and sandboxed upload | `Referrer-Policy: same-origin`; private files, signed links and their errors keep `no-referrer` |
| Uploaded content except raster images, audio, video, PDF and plain text, any delivery | `Content-Security-Policy: sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads` |

No JavaScript is needed: indicator tooltips are CSS drawn from `data-tip` on
hover and keyboard focus. The sandbox exists because the origin now holds a
session cookie: an uploaded page would otherwise run on Vela's origin and
could use a signed-in visitor's cookie to read and leak private files. In the
sandbox, scripts still run but in an opaque origin without cookies, storage or
same-origin reads, and cross-site posts are refused by the origin check. The
exemptions are a list of passive types rather than a list of dangerous ones,
so an unusual type that a browser renders as a document (SVG, XML,
`multipart/x-mixed-replace`) is still sandboxed; PDF is exempt because a
sandbox breaks built-in viewers. In accel mode the policy travels as
`X-Vela-Content-Security-Policy`. Without web sign-in, uploads keep their
previous behavior (SPEC §19.8).

## Logging

The access log adds the routes `login` and `logout` and records the key ID of
signed-in requests. Request bodies, the `Authorization` header, cookies,
session IDs and query strings are never logged; a form parse error is not
logged either, because it could quote the key.

## Verification

Service tests cover sign-in, expiry, revocation, the janitor and every prefix
rule on real SQLite with a fake clock. HTTP tests cover the routes, cookie
attributes, `next` validation, origin checks, the rate limit, caching and
policy headers, anonymous output against the golden fixtures, the sandbox, and
accel headers. `make test-e2e` signs in through the reference nginx and checks
that each header arrives once, nothing internal leaks, and no key, query or
session ID reaches a log.
