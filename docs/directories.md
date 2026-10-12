# Directory policy contract

Directory policies control public navigation and crawler hints independently
of files. Hidden directories are not access control: anyone who knows or guesses
the URL can see an enabled index. Public and unlisted file URLs remain accessible;
use private files with signed links for sensitive content.

[`SPEC-dirs.md`](SPEC-dirs.md) defines the extension and its accepted decisions.
DTOs and merge patches live in [`api/dirs.go`](../api/dirs.go), public Go client
methods in [`client/dirs.go`](../client/dirs.go), resolution and mutations in
[`internal/service/dirs.go`](../internal/service/dirs.go), and persistence in
[`internal/store/dirs.go`](../internal/store/dirs.go).

## Resolution and public behavior

A prefix is empty for root or ends in `/`; paths share the existing NFC,
reserved-name, segment, and length validation. Policies can exist before files,
and a rule at `a/` is legal even when an object named `a` exists.

| Setting | Resolution |
| --- | --- |
| `index` | Nearest `on`/`off` rule, then `VELA_ROOT_INDEX` (`on` by default) |
| `hidden` | Own rule only; default false |
| `noindex` | Nearest `on`/`off` rule; otherwise on under any hidden ancestor, off elsewhere |
| `title`, `description` | Own rule only; empty by default; plain text |

An explicit crawler override takes precedence over the hidden-derived default,
even when another hidden directory appears farther down that subtree. Titles
and descriptions have the same Unicode character limits as object metadata.
A root DB rule overrides the configuration fallback; startup logs its final
value and source. Root cannot be hidden.

Disabled indexes and absent directories use the same bounded public-existence
lookup and return the same themed 404 and headers. Rejection does not fetch
object metadata or build a subtree; slashless directory URLs use that bounded
lookup as well. A non-root index exists only when its subtree contains
a live public object. An enabled root preserves its empty index. Slashless URLs
redirect only to existing enabled indexes. Files retain their visibility/access
rules. Authenticated object operations are unaffected by directory policies.

The parent's page includes a child only if it is not hidden, its index is on,
and it has visible entries. Counts describe that child's files plus eligible
child directories; modification dates include only reachable visible entries.
With no stored rules, legacy subtree file counts and HTML are preserved.
Breadcrumb and parent links to disabled ancestors are omitted; names remain text.
HTML and `?format=json` use the same filtering. `robots.txt` never lists hidden
prefixes and its configured allow/deny behavior is unchanged.

Crawler exclusion uses `X-Robots-Tag: noindex, nofollow` on index pages and public
files, plus the HTML robots meta tag on index pages. Unlisted and signed content
retain their existing crawler exclusion. Cache-Control is unchanged. Accel uses
`X-Vela-Robots` through the existing nginx mapping. R2 proxy emits the header
directly; R2 redirect emits it only on the 302, not the external storage response.
The 302 retains its existing private/no-store caching; the external object grant
retains the public-file cache policy.
Existing browser/CDN cache entries can remain until their normal expiry.

## HTTP, authorization, and concurrency

`GET /api/v1/dirs` lists rules with `prefix`, `recursive` (default true),
`orphaned`, `limit`, and `cursor`. Nonrecursive lists include the prefix's own
rule and its immediate child rules. Pagination follows byte-order prefixes;
root is a valid first entry. Orphaned means no live public objects below the rule.

`GET /api/v1/dirs/{prefix...}` returns `EffectiveDir`, even without a rule.
Root is `/api/v1/dirs/`; a missing trailing prefix slash is normalized without
a redirect. `url` is null when the index would return 404; `listed_in_parent`
reflects index enablement, hidden state, visible content, and the parent index.
Inheritance sources are returned unchanged, including ancestors outside a key's
prefix. Sources describe settings, not file contents.

PUT fully replaces a rule, using defaults for omitted fields; PATCH applies
JSON Merge Patch and creates defaults when absent. PATCH null resets a field.
PUT returns 201 for creation and 200 for replacement; PATCH returns 200.
DELETE removes only the rule and returns 204, or 404 if absent. Responses with
a stored rule carry revision ETags (`"r<number>"`); `If-Match` guards mutations,
and `If-None-Match: *` can enforce creation. Preconditions are checked inside
the write transaction. API responses are not cached.

`ro` can read; `rw` can write within its prefix, including the prefix itself.
Root mutations require an unrestricted `admin`. Collection reads narrow to a
key's prefix; individual reads outside it return 403. Unknown/body service fields,
invalid modes, root hidden=true, and oversized metadata are invalid requests.
The schema has no foreign key to objects: trash, restore, and janitor preserve
policies. SQLite backups include them automatically.

Recursive moves change policy prefixes atomically with object paths. A target
with any nonmoving rules conflicts even if none occupy a mapped source name.
Without overwrite, `409 conflict` includes `details.conflicting_policies` and
rolls back everything. Overwrite replaces the entire target policy set; file
moves leave policies alone. Policy-only prefixes can also move. Source/target
key boundaries apply; moving a rule onto root requires unrestricted admin and
must satisfy root validation. Responses add `policies_moved` to `moved`.

All rules load at service startup into an immutable map. A persistent SQLite
counter, advanced by insert/update/delete triggers in the same transaction,
invalidates local and cross-process readers. Each operation checks the counter;
local mutations synchronously invalidate after commit. After the complete
batched object read, the service rechecks that generation and retries the listing on a change. This prevents a concurrent cross-process
move from combining new object paths with old hidden rules; theme fields and
visible counts/dates use the validated policy snapshot. For an enabled, existing
public index, one repository listing reads its live public objects in SQL batches, then computes visible entries and dates in
memory. It does not query recursively per child; a policy-generation change
restarts the complete listing.

## Hidden entrance scenario

These settings disable the root, enable a known entrance with ordinary visible
children inside it, and disable a raw-file directory's index:

```sh
vela dir private-root
vela dir hide test0/
vela dir set test0/raw/ --index off
vela dir show test0/
```

The root returns 404; `/test0/` and public subdirectories open directly.
`/test0/raw/` returns 404 while its public files still open by URL. `dir show`
reports the effective sources, index URL/404, parent listing, and crawler hints.
`dir ls --orphaned` identifies saved settings whose public content is gone;
`dir rm /` asks for confirmation because it may re-enable the root fallback.
`ls -l` annotates child rules, `mv` reports moved policies, and directory `url`
resolves the actual index availability. `open` uses the same URL resolution.
Use each command's help and `--json` for the supported interface.

When an older API v1 server lacks `/api/v1/dirs`, existing commands retain their
legacy behavior: `ls -l` omits policy annotations and directory `url`/`open`
resolve through `/info`. Only a missing collection endpoint enables this
fallback. A directory lookup 404 is checked against collection support first;
authorization/transport failures and disabled-index results remain errors on
supporting servers. New `dir` commands require directory-policy support.

## Theme compatibility and verification

Custom themes must handle empty breadcrumb URLs as text, suppress empty parent
URLs, and render the robots meta tag when `Page.NoIndex` is true. The parent
remains a URL string to preserve existing JSON. See [theme.md](theme.md).

Pre-extension HTML fixtures in `internal/server/testdata/index/` verify unchanged
rendering without rules. Public JSON checks preserve old fields while allowing
new theme metadata. Tests cover inheritance, CRUD/preconditions, scope/prefix
boundaries, visible counts/dates, identical 404s, cache refresh through independent
SQLite pools, concurrent reads/updates, moves, backups, and CLI output. Nginx e2e
checks header uniqueness and internal-header suppression; MinIO tests exercise
proxy/redirect crawler headers. Run the verification targets in CONTRIBUTING.md.
