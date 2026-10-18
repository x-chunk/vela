# Web sign-in for the Vela index (spec)

Status: draft. This is the first step toward a possible web admin panel: key-based sign-in with read-only browsing. File management in the browser is out of scope here and will be specified separately if it is ever needed.

## Goal

Let the owner open the web index with an API key and see everything that key can see: unlisted and private files, hidden directories, directories with the index disabled. Anonymous behavior does not change at all.

Non-goals: file management in the browser, user accounts, ACLs, OAuth, "remember me", multiple roles per session.

## Model

- The web session is a thin wrapper around an existing API key. No new credential type.
- Any key (`ro`, `rw`, `admin`) can sign in. The web session is always read-only, regardless of the key scope.
- The session inherits the key's path prefix.
- The key is re-checked on every request. Revoking or rotating a key, or changing its prefix, takes effect immediately.
- Feature flag `VELA_WEB_AUTH` (default off), so an upgrade is a no-op until enabled.

## Prefix semantics

Visible tree = anonymous view + the full view of the key's prefix subtree.

- **No prefix:** everything is visible, including hidden directories and directories with the root index disabled.
- **Prefix `test/`:**
  - Inside `test/` (recursive) every file is listed and downloadable regardless of visibility, and directory policies (hidden entrance, index disabled) are ignored.
  - Ancestors of the prefix (the root for `test/`, `a/` for `a/b/`) are listed exactly as for anonymous, plus one extra entrance: the single child on the path to the prefix, even if that child is hidden or its parent has the index disabled. No other hidden sibling is revealed.
  - Everything outside the prefix behaves exactly as for anonymous, including status codes. No 403s that reveal existence: a path the session cannot see is a 404.
- Match on path segments, not string prefixes: a key for `test/` never reveals `test2/`.

## Sign-in

- Routes use the existing reserved namespace `/_vela/` (assets already live in `/_vela/assets/`):
  - `GET /_vela/login` renders the form.
  - `POST /_vela/login` accepts the key in the request body.
  - `POST /_vela/logout` ends the session.
- **The key is never accepted in a URL.** No `?token=` or `?key=`. Such parameters are ignored, not logged and not echoed. The form field is `type=password`, `autocomplete=off`.
- After a successful login respond `303` to `next`. `next` must be a relative path starting with a single `/` (reject `//`, schemes and backslashes), otherwise redirect to `/`.
- Failed login: one generic message, same response time (compare hashes, constant-time). Rate limit per client IP, e.g. 5 failures per minute, then `429` with `Retry-After`. Trust `X-Forwarded-For` only from the configured proxy.
- Request bodies and `Authorization` values are never written to access logs.

## Session and cookie

- Session ID: 256 bits from `crypto/rand`. The cookie carries only this ID. The server stores its SHA-256 plus key ID, created, last seen and expiry in SQLite. The API key itself is never stored in or derived from the cookie.
- Cookie: `__Host-vela_session`, `Path=/`, `Secure`, `HttpOnly`, `SameSite=Lax`, no `Domain`. Lax so that links opened from chats and notes still arrive signed in. Every state-changing endpoint is `POST`, so Lax is sufficient.
- Dev over plain HTTP: when `VELA_WEB_INSECURE_COOKIE=1`, use the name `vela_session` without `Secure`. Never enabled by default.
- TTL: sliding idle timeout 7 days, absolute maximum 30 days. Both configurable (`VELA_WEB_SESSION_IDLE`, `VELA_WEB_SESSION_MAX`). Update `last seen` at most once per hour to avoid write amplification.
- Logout: delete the server-side row and expire the cookie. `POST` only, with a same-origin `Origin` / `Sec-Fetch-Site` check.
- The janitor purges expired sessions. A `velad` subcommand to revoke all sessions is nice to have.

## Caching

- Anonymous responses: unchanged. Add `Vary: Cookie` to every response whose content can differ by session (listings, file downloads, 404s under those paths).
- Any response produced for a valid session (listings, login page, file bodies): `Cache-Control: private, no-store`.
- The decision stays in Go. With nginx accel, the `X-Accel-Redirect` response must carry the same headers, and the proxy must not cache these responses.
- If a CDN sits in front (Cloudflare caches by file extension and ignores `Vary: Cookie`), add a cache bypass rule when the `__Host-vela_session` cookie is present.
- `Referrer-Policy: same-origin` on all HTML responses.

## Content Security Policy

The current index policy sets `form-action 'none'`, which would block the login and logout forms. Rules:

- Anonymous index and error pages: unchanged, byte for byte. The footer "sign in" is a plain link, not a form.
- `/_vela/login` and any page rendered for a signed-in session: the same policy with only `form-action 'self'`. Nothing else is relaxed: no inline styles, no inline scripts, no remote resources.
- This means no JavaScript is needed for any of the new UI.

## UI

Principle: do not redesign. New elements reuse the existing templates, `style.css`, theme variables and the built-in `icon` helper, work in light and dark modes, and need no JavaScript. Anonymous pages render identically to today.

- **Login page:** the base layout, one password field and one button, nothing else.
- **Entry point:** a small "sign in" link in the footer (not the header).
- **Signed-in state:** in the same footer place show the prefix (or "full access") and a "sign out" button (a `POST` form styled like the footer link).

### Visibility indicators (icons, not text)

Visible only in signed-in sessions. Each indicator is a small inline SVG from the same icon set and in the same style as the existing built-in icons (same viewBox, stroke weight and corner style, `currentColor`, muted color). It sits right after the name. No new colors, no text labels, no badges with borders.

| State | Applies to | Icon name | Tooltip |
| --- | --- | --- | --- |
| `private` | file | `lock` | Private: needs a key or signed link |
| `unlisted` | file | `unlisted` (link) | Unlisted: anyone with the URL can open it |
| `hidden` | directory | `hidden` (eye-off) | Hidden: not shown to anonymous visitors in the parent listing |
| `no-index` | directory | `no-index` | Index disabled for anonymous visitors |

- Public files get no indicator, to keep the list quiet. A directory can show both `hidden` and `no-index`.
- The exact geometry of the four new icons is drawn to match the existing ones in `internal/index/theme/`. Reuse their conventions instead of inventing new ones.
- **Tooltips:** CSS only, defined in `style.css` (inline styles and scripts are blocked by the CSP). The icon is an element with a `data-tip` attribute and `tabindex="0"`. The tooltip appears on hover and on keyboard focus, uses the existing background, text color, border and radius, and is small and single-line. Every indicator also has `aria-label` with the same text, and `title` is not used so the native tooltip does not double up.
- Tooltips must not overflow the viewport on mobile: on narrow screens they align to the right edge of the row.

### Template and JSON contract

- `Dir` and `File` get a `Flags` list (strings such as `private`, `unlisted`, `hidden`, `no-index`), empty for anonymous. `Page` gets `Session` (nil for anonymous) with `Prefix` and `Full` (no prefix).
- New helper `{{flagIcon "private"}}` renders the icon with its tooltip markup, so custom themes can opt in with one call.
- The statement in the theming docs that templates never receive private or unlisted objects must be amended: they are received only when the request carries a valid session. Keys, signatures and blob IDs are still never exposed.
- `?format=json` follows the same rule. The new fields are additive and not renamed within a major version. Anonymous JSON is unchanged.
- Themes without the new calls keep working: missing flags simply render nothing.

## Docs to update

- Introduction: replace "No web management UI ... browser only sees the read-only public index" with a note on optional read-only sign-in.
- Theming: new `Flags`, `Session`, `flagIcon`, CSP note, the data contract amendment.
- A page on web sign-in covering prefixes, TTL, caching and the CDN rule.

## Acceptance checks

1. Anonymous output is byte-identical to before, apart from the footer link and `Vary: Cookie`. Anonymous CSP is unchanged.
2. Key with prefix `test/` where `test/` is hidden: the root shows the `test/` entrance only (with the `hidden` icon), contents are fully visible, a sibling hidden directory stays invisible and returns 404.
3. Prefix `a/b/` where `a/` has the index disabled: `a/` lists only `b/` (plus anonymous-visible entries).
4. Prefix `test/` does not expose `test2/`.
5. `?token=...` has no effect and does not appear in logs.
6. Revoking the key makes the next request anonymous.
7. Idle and absolute expiry both end the session. Logout invalidates the server-side row, not just the cookie.
8. A signed-in response carries `private, no-store` and `form-action 'self'`. A shared cache never serves it to an anonymous request.
9. Open redirect: `next=//evil.example` and `next=https://evil.example` redirect to `/`.
10. Brute force from one IP gets `429` after the threshold.
11. Tooltips show on hover and keyboard focus in light and dark modes, with no inline styles or scripts.

## Open questions

- Whether to ship the revoke-all subcommand in the first iteration.
- Whether idle 7d / max 30d suits the single-owner use case or should be shorter.
- Final icon names (`unlisted`, `hidden`, `no-index`) once checked against the existing icon set.
