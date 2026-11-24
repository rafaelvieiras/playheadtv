# Architecture

This document captures the backend design decisions behind
[`migrations/0001_init.sql`](../backend/migrations/0001_init.sql) and the
`internal/domain` package. It assumes the client-side channel assembly
principle from the [README](../README.md) — the backend never touches video,
only scheduling metadata.

## Tenancy and pricing tiers

Two ways to run Playhead TV, same code and schema for both:

- **Self-hosted** (free): the user runs the backend on their own infra. A
  single implicit `tenant` row.
- **Cloud-sync** (paid, ~US$9/year reference price): for users who don't want
  to host anything (e.g. they only have access to someone else's shared
  Jellyfin/Plex). One `tenant` row per paying customer, multi-tenant from the
  start — the schema never assumed single-tenant and grew a tenant column
  later. Self-hosted users can also pay the same fee as an optional donation;
  same flow, no functional difference.

## Storage backend

- **Self-hosted default**: a local SQLite file. Zero external dependency.
- **Cloud-sync**: [Cloudflare D1](https://developers.cloudflare.com/d1/), a
  managed, globally-replicated SQLite, reached over its **HTTP query API**
  from a normal long-running Go process. The backend does **not** run inside
  a Cloudflare Worker/V8 isolate — Go has no native Workers runtime. D1 is
  just an external database over HTTPS here, chosen for low operational
  overhead (no Postgres instance to run/back up).
- Both are exposed behind one `storage.DB` interface
  (`backend/internal/storage/db.go`) so the domain layer never branches on
  deployment tier.

## Backend resource constraints

Driven by the cloud-sync tier running on a metered, serverless-adjacent
database (D1):

- The backend only stores and syncs metadata — no video processing, and no
  heavy computation in the request path either (see "EPG materialization"
  below: the one expensive computation is done asynchronously, ahead of
  time, never on a `GET`).
- Reads are cache-first. `GET /epg` is the highest-traffic endpoint by far
  and is designed to be servable from a CDN cache without touching D1 at all
  (see "Edge caching").
- No long-lived connections (no WebSocket). Sync between devices doesn't
  need one — see "Why no realtime sync is needed" below.

## Authoring happens in the web UI + MCP, not in playback clients

Channel/block/schedule authoring is done through the backend's own web UI and
through MCP tools (`github.com/modelcontextprotocol/go-sdk`) — both operate
on the same REST-backed data layer. Playback clients (desktop/mobile/TV) are
kept as thin as possible: resolve the materialized EPG, play it with
preload+crossfade, and optionally relay catalog metadata (see "Media
sources" below). This was a deliberate scope cut: better authoring UX on a
proper web UI, and MCP tools need to operate on this data regardless, so
duplicating authoring UI into every client platform bought nothing.

### MCP surface: everything the web UI can do

The MCP server (`/mcp`, `internal/mcpserver`) covers every REST operation the
web UI uses, one tool per operation. Two kinds of tool exist:

- Hand-written tools that share a domain package with the REST handlers
  (`internal/cutruleset`, `internal/breaktemplate`, `internal/blockitem`,
  `internal/catalogsync`, `internal/series`).
- Thin REST-backed tools (`rest.go`, `rest_tools.go`): the tool builds a
  request for the REST route and runs it in-process through
  `api.RequireAuth(mux)` with the caller's own `Authorization` header (no
  network hop). Validation, status codes and error messages, the
  materializer trigger of a schedule-changing write, and the auth decision
  are therefore the REST handler's, with no second implementation to keep in
  step. A REST error comes back as a tool error carrying its message and
  status; a list result is `{"items": [...]}`.

Conventions:

- **Destructive tools** (every `delete_*` and `reset_channel_epg`) require
  the argument `confirm=true`; without it the call is rejected with a clear
  message before anything runs. The convention is in each tool's description
  and input schema.
- **Authorization**: the REST layer has no permission model beyond "a valid
  app_user session" (there is one user per tenant; device tokens are refused
  on everything but the playback reads). Devices, stream preferences and
  media source credentials therefore need exactly that session, over MCP as
  over REST, and a device token cannot reach `/mcp`. A real permission model
  belongs to the Auth roadmap item and will apply to both surfaces at once.
- **Secrets**: read tools never return media source credentials or device
  tokens (the domain types do not serialize them). A credential is
  write-only (`register_media_source`, `update_media_source`); a device
  token is returned once, by `create_device`.
- **Parity is enforced**: `mcpserver.RouteTools` maps each REST route to its
  tool and `NoMCPCounterpart` lists the routes deliberately without one
  (login/logout, bootstrapping the user, the password reset consume route,
  `GET /me`, the preference staleness poll), each with a reason. `TestEveryRESTRouteHasAnMCPTool` takes the route
  list from `api.Routes()` (recorded from the same code that registers the
  routes) and fails on a route in neither list, so a new web UI capability
  cannot ship without its tool.

## Admin shell

The admin SPA (`webui/`) is served by the Go binary under `/admin/`, on the
same origin as the API, and it is a surface an operator has to be able to use
from a phone as well as from a desktop. Four decisions make that true, and
each one replaced a defect measured in a Playwright pass:

- **The sidebar is a drawer below `md`.** A fixed 224 px sidebar left `main`
  166 px wide on a 390 px viewport, which clipped the page title, the primary
action and every table. `NAV_ITEMS` (`components/Layout.tsx`) is rendered
twice — by the `md:` sidebar and by the drawer over `components/ui/sheet.tsx`
— and never as a second literal list.
- **The shell owns the viewport, `main` owns the scroll.** `h-svh` on the
container with `overflow-y-auto` and `min-w-0` on `main`; without it the
sidebar stretched to the document height and scrolled away on a long page,
leaving an empty column, and the document itself scrolled instead of the
content column.
- **One save contract, and it is where the eye is.** `components/SaveBar.tsx`
is rendered by every editor that has unsaved edits. It is `fixed` over the
bottom edge — a `sticky bottom-*` bar only pins while its natural position is
below the fold, and six editors place it mid-page, where it scrolled away
exactly when it was needed — and it owns the two leave guards:
`useBlocker` for in-app navigation and `beforeunload` for a reload or a closed
tab. The route table is therefore a **data router** (`createBrowserRouter` in
`src/router.tsx`); `useBlocker` does not exist on a plain `BrowserRouter`, so
reverting that migration would silently drop the in-app guard.
- **A 4xx is a final answer.** The `QueryClient` retries 5xx only. The default
of three attempts turned every 404/409 into 7–10 s of "Loading…" before the
same message (a bogus collection id cost four requests).

Two smaller rules keep the phone usable without per-page work: the primitives
carry a 44 px touch floor below `sm` (`max-sm:min-h-11` on `button`,
`max-sm:h-11` on `input`/`select`/`combobox`/`tabs`), and `--ring` is the
foreground value because every `ring-ring/50` utility halves it — the light
grey token it replaced was ≈1.5:1 against white, below the 3:1 non-text
contrast minimum.

## Password recovery

There is one `app_user` per tenant and no e-mail, so a forgotten password is
recovered by whoever controls the server, in two halves.

1. **`server reset-password [--email <address>] [--ttl <duration>]`** (same
   binary as `server healthcheck`; in a container,
   `docker exec <container> /server reset-password`, no shell needed in the
   distroless image). It stores a random single-use token and prints it once,
   with the page that accepts it. The token lives in the `password_reset` table
   as a SHA-256 hash plus an expiry, one row per user: running the command
   again replaces the pending token. `--ttl` defaults to 15 minutes and is
   bounded to 1 minute - 1 hour. `--email` is optional while exactly one user
   exists and required afterwards. The command never prints or stores a
   password and never writes the token to a log; the token goes to stdout only.
2. **`POST /password-resets/consume`** with `{token, new_password}`
   (unauthenticated, like login; the token is the credential). It applies the
   same minimum length as `POST /app-users`, compares the presented token's
   hash with every pending hash in constant time (`crypto/subtle`), then in one
   transaction sets the new bcrypt hash, deletes **every session** of the user
   and deletes the token. Device tokens are kept (they are the playback
   clients' credentials) and the response says so. An unknown, expired, used or
   burned token all answer the same `400`. Abuse limits: at most 10 attempts
   per client address and 5 per token value per 15 minutes (`429`, in memory;
   the address is the connection peer, forwarding headers are not trusted), and
   5 wrong tokens burn the pending one (run the command again). The route has
   no MCP counterpart on purpose (`NoMCPCounterpart`).

The web UI page `/admin/reset-password` takes the token from the URL fragment
(`#token=...`, which a browser never sends to a server, so no access log or
proxy sees it), from a query string, or from a field, plus the new password
twice. It removes the token from the address bar on load, the admin responses
carry `Referrer-Policy: no-referrer`, and it links to sign-in afterwards.

**How the command reaches SQLite.** It opens its own short-lived connection to
the file named by `PLAYHEADTV_DB_PATH`, not an admin channel of the running
server. The database is in WAL mode, so a second process can write beside the
server; `storage.Open` sets `busy_timeout` (5 s) so either side waits for the
other's short write instead of failing. The server's pool stays at one
connection (`SetMaxOpenConns(1)`). This needs no extra listener or credential
(having the data directory is the authority), works with the server running or
stopped, and works from outside the container. The command never creates a
database (a missing file is an error) and applies migrations only when the
`password_reset` table is absent, as the server would at its next boot.

## Media sources

A `media_source` is a Jellyfin/Plex/local-folder library, or a **plugin-backed**
source (below). Only `jellyfin` and `plugin` are implemented in v1; the `type`
column and `catalog_item` cache are already source-agnostic so adding
Plex/local-folder later doesn't touch scheduling tables at all. **A channel can
mix items from different media sources in the same schedule** — every
scheduling table (`block_item`, `filler_pool_item`) references `catalog_item.id`,
never a source's native ID.

### Plugins (migration `0028`)

Some sources cannot be implemented in this repository: taking video from a
third-party site means an extractor that site changes constantly, and its legal
footing is not this project's to grant. The core therefore implements the
*host* half only — install, verify, run and talk to an out-of-process plugin —
and never any site-specific code. The full contract is
[`docs/PLUGINS.md`](PLUGINS.md); what matters architecturally:

- A plugin artifact is a `tar.gz` with a `plugin.json` manifest and an
  executable. Its SHA-256 (from the registry) is verified before extraction,
  and the manifest's id/version must match the registry entry that named it.
- The host protocol is JSON-RPC over the plugin's stdin/stdout: `info`,
  `catalog.list` (paged, shaped exactly like the push-sync payload),
  `media.resolve` (a short-lived playable URL), `shutdown`.
- `media_source.type = 'plugin'` names `plugin_id` (plus an optional
  `plugin_min_version` and its own `plugin_config_json`), and is always a
  **pull** source: the backend lists it on the same sweep that lists Jellyfin,
  so the grid materializes with no client online. The backend image never
  ships an extractor; installing a plugin is what adds one, at runtime.
- The backend is also a **caching mirror** of the registry: `GET /plugins`
  rewrites artifact URLs to `/plugins/{id}/{version}/{os}-{arch}`, which serves
  the verified bytes (ETag = SHA-256, immutable), so a desktop client installs
  from the backend it is already paired with.
- Because the client resolves URLs at playback time, `GET /epg` carries a
  `requires` list per entry (the plugin and minimum version behind its catalog
  item). A client without that plugin hides the channel instead of tuning into
  an error, and says so in Settings. Android cannot execute a downloaded file
  (W^X since API 29), so there the plugin is a separate APK the client binds to
  — reserved in the contract, not implemented yet.

### Reachability: pull vs push

Self-hosted backends and cloud-sync backends alike may need to browse a
user's Jellyfin catalog to power the authoring web UI — this is a stronger
requirement than the client's own "can I reach Jellyfin to play this file"
check, because now the *backend* needs connectivity to enumerate the catalog.
Self-hosted almost always can (same infra). Cloud-sync often can't: many
users only expose Jellyfin over a mesh VPN (e.g. NetBird) with no public
route.

`media_source.sync_mode` is decided automatically by a connectivity probe run
when the source is registered:

- **`pull`**: the backend queries the source directly, on demand or on a
  light periodic refresh.
- **`push`**: the backend can't reach the source. The web UI shows a prompt
  asking the user to open a Playhead TV client on a device that *can* reach
  it, to sync catalog metadata. `local_folder` sources on cloud-sync are
  always `push` by definition — there is no network path from Cloudflare to
  a home NAS filesystem.

Push sync is incremental, mirroring the EPG revision pattern in the opposite
direction (client → backend instead of backend → client): the client tracks
a local checkpoint per source (the latest `DateModified`/`updatedAt` it has
seen) and only uploads what changed since then via
`POST /media-sources/{id}/catalog-sync` (batched upserts/deletes). The
trigger is automatic and silent whenever the app is foregrounded with that
source reachable and the internet up, plus a manual "sync now" action.

Pull sync is the mirror image, and the backend does all of it itself:
`internal/pullsync.Syncer` (structurally the same background-worker shape as
`internal/materializer` — `Trigger`/`Start`/`Run`, plus a synchronous
`SyncOne` for the manual path) calls the real Jellyfin API
(`GET {base_url}/Items?Recursive=true&IncludeItemTypes=Movie,Episode,Video`,
paginated, authenticated with the source's decrypted credential, see "Jellyfin
authentication" below), maps `Movie`/`Episode` items into `catalog_item`
(`RunTimeTicks / 10_000 = duration_ms`; `SeriesName` only on episodes).
`Video` is what Jellyfin tags every item in a `homevideos`-type library
(no dedicated item type for that kind of clip) — mapped to
`CatalogOther`, with no genre/series metadata to speak of, good enough for
curating into a `filler_pool` by hand (e.g. a network's bumpers/
interstitials/promos). Pull sync also
deletes any `catalog_item` row whose `external_id` no longer appears in the
response — no incremental checkpoint like push's `DateModified` diffing, a
self-hosted library is small enough that a full re-list every sync is
simpler and always correct. Runs on a fixed 15-minute periodic tick
(`pullInterval`, not configurable — same rationale as
`materializer.tickInterval`) plus an opportunistic, non-blocking trigger
right after `POST`/`PUT /media-sources` results in a jellyfin source in pull
mode, and a manual "Sync now" button in the web UI
(`POST /media-sources/{id}/pull-sync`, synchronous — returns the item
upsert/delete counts directly). The upsert/delete/`last_synced_at` SQL is
shared between push and pull via `internal/catalogsync`, not duplicated.
Also captures `genres`/`production_year` on every item: for a Movie both come
straight off the item itself, but an Episode's own `Genres` always comes back
empty from Jellyfin, so its genre is inherited from the parent series via one
extra `GET /Items/{seriesId}` per unique `SeriesId` seen that sync (a `userId`
is required on that call by this Jellyfin version, resolved once per sync
from the first entry of `GET /Users`).
Both syncs have an MCP tool (`sync_media_source_catalog`,
`pull_sync_media_source`) for parity with the web UI.


#### Jellyfin authentication

Jellyfin 12 disables the legacy authorization mechanisms by default: the
`X-Emby-Token` and `X-MediaBrowser-Token` headers and the `api_key` query
parameter answer `401`, and the `/emby/` route prefix is gone (`404`). Only the
current forms are used, so the same code works on 10.10+ and 12.x:

- **Backend** (`internal/pullsync`): every request goes through
  `newJellyfinRequest`, which sends `Authorization: MediaBrowser Token="<key>"`.
  No call may set a legacy header or query parameter. A `401` wraps
  `ErrJellyfinAuth` ("jellyfin rejected the API key"), which `SyncOne` returns
  as is, so `POST /media-sources/{id}/pull-sync` answers `502` with that
  message and the background worker logs it. On `GET /MediaSegments/{id}` a
  `401` is an auth error too (the lookups stop and the log says why); only
  `403`, `404`, `405` and `501` mean "no segments API for this key/server".
- **Client**: libmdk takes only a URL, so the key stays in the query string,
  spelled `ApiKey` (`streamUrl`, `jellyfinImageUrl`; the mosaic preview, the
  EPG art and the art cache reuse them). `api_key` is not accepted by 12.x.
- `GET /Users` with the key and the mandatory `userId` on `GET /Items/{id}`
  behave the same on both versions.
- Tested against Jellyfin 10.11.11 and 12.2.0 (minimum supported: 10.10, which
  introduced media segments). The stream and primary-image routes are
  anonymous on these versions, so the client's `ApiKey` is about staying valid
  if that changes, not a measured requirement today. An operator who cannot
  move yet can set `EnableLegacyAuthorization` to `true` in the Jellyfin
  server configuration as a stopgap; upstream marks it temporary.

### Catalog metadata (migration `0009`)

Beyond what scheduling needs, `catalog_item` caches descriptive metadata so
the UI, MCP and later criteria matching can use it. Both sync directions
write it through `catalogsync.Upsert`, which replaces the whole stored
snapshot of the item (a field a sync leaves out is cleared).

- **Single-valued facts are plain nullable columns:** `season_number` (0 =
  specials), `episode_number`, `episode_number_end`, `series_external_id`,
  `season_name`, `original_title`, `sort_title`, `premiere_date` and
  `date_added` (epoch ms, UTC), `overview`, `tagline`, `content_rating`,
  `community_rating`, `critic_rating`, `imdb_id`/`tmdb_id`/`tvdb_id`, and the
  technical info `width`, `height`, `video_codec`, `container`, `bitrate`.
  Season/episode numbering and `series_external_id` are mapped for episodes
  only.
- **Art is references only:** `image_primary_tag`, `image_backdrop_tag`,
  `image_thumb_tag` and `series_image_primary_tag` (the parent series' primary
  tag, a fallback for episodes) are Jellyfin image tags. The backend never
  fetches, stores or serves images; clients download and cache art from the
  media server like they do video.
- **Everything queryable is normalized and per tenant:** dimension tables
  `person`, `studio`, `tag`, `genre`, `language` (columns `tenant_id`, `id`,
  `name`, `name_norm`; unique on `(tenant_id, name_norm)`, where `name_norm`
  is `catalogsync.FoldText`, so spelling differing only in case/accents is one
  row and the first spelling seen is kept for display) and link tables
  `catalog_item_person` (`kind` actor/director, `role`, billing `position`),
  `catalog_item_studio`, `catalog_item_tag`, `catalog_item_genre`,
  `catalog_item_language` (`kind` audio/subtitle). `tenant_id` leads every
  primary key and index, in both directions of each link, and links cascade
  with their `catalog_item`. Dimension rows nobody links to any more are not
  garbage-collected.
- **JSON only for descriptive data:** `production_locations` (array of
  strings) and `chapters` (array of `{name, start_ms}`).
- **People are capped** at `catalogsync.MaxPeople` (10) per item, directors
  first, then main cast in the source's order, enforced for push and pull alike.
- The comma-separated `genres` column is unchanged and still what dynamic
  blocks and pools match against. `catalog_item_genre` is filled in parallel
  from it and nothing reads it yet.
- Re-syncing an item diffs its links per table: stale links are deleted, new
  ones inserted, a changed role/position updated, unchanged links cost no write.

Pull sync requests, in addition to the original `Fields`, `Overview`,
`Taglines`, `OriginalTitle`, `SortName`, `ProviderIds`, `Studios`, `Tags`,
`People`, `ProductionLocations`, `Chapters`, `MediaStreams` and
`MediaSources`. Technical info comes from item-level `Width`/`Height`/
`Container` when present, else the first video stream and first media source;
audio/subtitle languages from the item's streams. Episodes inherit from their
series (the same single `GET /Items/{seriesId}` per unique series that
already provided genres, now with `Fields=Genres,Studios,Tags,People,
ProductionLocations`): genres, studios, tags, content rating, production
locations, directors and cast (each only when the episode has none of its own)
and the series' primary image tag. Overview, ratings, dates, provider ids and
numbering are never inherited. Cost of requesting `People`/`MediaStreams`/
`MediaSources` on a large library's first sync has not been measured.

Push sync (`POST /media-sources/{id}/catalog-sync`) accepts the same fields
optionally, using the response field names (plus `genres` and
`production_year`), with `cast: [{name, role}]` and `directors: [{name}]`.

`GET /catalog-items` returns every scalar field plus the compact lists
(`studios`, `tags`, `audio_languages`, `subtitle_languages`; omitted when
empty). `GET /catalog-items/{id}` and the MCP `get_catalog_item` tool
additionally return `cast`, `directors` (each `{name, type, role}`),
`production_locations` and `chapters`. MCP `search_catalog` returns the list
shape. Title search is still a table scan over `search_text`, moving it to
FTS5 is a follow-up once catalogs grow.

`media_source.last_synced_at` is surfaced in the web UI (both pull and push)
so staleness is visible, not hidden.

### Catalog queries (migration `0012`)

A query is a versioned JSON condition tree, the one stored form of a
collection's query (see "Collections"; before migration `0014` it lived in
`block.criteria`, `filler_pool.criteria` and the spec of a `criteria` block
item): `{"version": 1, ...}` plus exactly one of `all: [node]`, `any: [node]`,
`not: node` or a condition `{field, op, value}` at every level. Parsing is
strict (`catalogsync.ParseQuery`): unknown keys, fields or operators, wrong
value shapes, empty groups and trees deeper than 6 levels or larger than 64
nodes are rejected, and the stored form is the canonical re-serialization. Every
value is a bind argument and every identifier comes from a fixed field table,
so compiling to SQL (`Query.Where`) is injection-safe. The old fixed shape is
refused with a pointer to the new one.

Fields and operators:

- Curated lists, exact match on the normalized name (`FoldText`, so case and
  accents are ignored) through the link and dimension tables of migration
  `0009`, never a substring: `genre`, `studio`, `tag`, `audio_language`,
  `subtitle_language`; operators `equals`, `in`.
- Free text, accent- and case-insensitive `contains` (`%`/`_` are literal):
  `title` (the catalog search text, so it also reads the series title),
  `overview` (the folded copy in `catalog_item.overview_norm`), `person`
  (cast or director).
- Enumerations, exact: `kind` (movie, episode), `content_rating`; `equals`, `in`.
- Numbers: `year`, `duration` (minutes), `community_rating`, `critic_rating`,
  `season_number`, `episode_number`; dates `premiere_date`, `date_added`
  (`YYYY-MM-DD`, UTC, compared by whole day); operators `equals`, `lt`, `lte`,
  `gt`, `gte`, `between` (inclusive).

A condition never matches an item that lacks the field, so `not` of a
comparison includes such items. Dynamic blocks and pools resolve a query to
every match ordered by title, as before.

Startup backfills (`search_text`, `overview_norm`, genre links) run
synchronously, before the server listens, in batched transactions
(`catalogsync.BackfillBatchSize` rows per commit through `storage.DB.Transact`),
with their own 30-minute budget independent of the short deadline of the rest
of startup, logging progress. They are resumable and idempotent (only rows
still missing the derived data are touched, in id order). Running them before
listening, instead of in the background, was chosen because queries and the
materializer would otherwise see half-filled derived data (a genre or text
match silently missing), which is worse than a slower first boot; batching
makes that boot take seconds per 50k items.

Migration of existing criteria: SQL cannot compare matched sets, so
`catalogsync.MigrateLegacyCriteria` runs at startup (after backfilling genre
links and `overview_norm` for rows that predate them). For each legacy value it
translates to a tree (genres become an exact `genre` condition) and compares the
matched set before (the old substring rule) and after. Equal sets: the value is
rewritten. Any difference, or an untranslatable value: nothing is applied, the
legacy value keeps resolving as before, and a `criteria_migration_review` row
records both counts and the items that would be added or dropped. They are listed by
`GET /criteria-migration-review` (MCP `list_criteria_migration_review`) and the
block and pool editors; applying one is a normal PUT with the proposed (or an
edited) query, which clears the row. A held value is re-evaluated at every
startup.

Criteria block items (`item_type = 'criteria'`, `block_item.spec` =
`{collection_id, playback_order, seed, amount}`; see "Collections") live in manual blocks next to catalog
and series items. A visit airs `amount` matches (`{mode: count, count: N}`,
required, or `{mode: all}`, which must be chosen explicitly: every match once),
then the rotation moves on. Orders: `title` (the default), `release_date`,
`random`, `shuffle`. The materializer walks them with the same cursor as a
series (`seriesRun`: `series_cursor`, `epg_entry.source_block_item_id` and
`series_cycle`, seed mixed with the channel id), the matches playing the part
of episodes (items with a non-positive duration are left out), so `all` is "the
rest of the playback cycle". The collection's query is re-evaluated on every materializer
run. Changing the order restarts the item; changing the collection or amount keeps
the cursor. A random item can differ between a dry-run and a later reset.

**Interleaved by series** is a fifth order (`interleaved`) for criteria items
and for dynamic blocks: round-robin across the series the matches belong to.
Each match belongs to a series (media source plus external id, else title; a
movie, a bumper or an episode with no series is a one-item series). A round
visits every series once in turn, each airing `per_round` episodes (default 1)
in natural order (season, episode; specials first, missing numbers last); the
rounds repeat. `series_order` sequences the series within a round: `title`
(default), `release_date` (earliest premiere), `shuffle` (fixed within a
cycle) or `random` (fresh each round, never opening with the series that
closed the previous round). `on_exhaustion` is `restart` (default) or `drop`
and says what happens when one series runs out of episodes:

- `restart`: that series restarts from its own first episode, on its own, and
  keeps taking its turn in the following rounds while longer series continue.
  Short series loop repeatedly and the rotation never degenerates to the
  longest series alone; the rounds never reset. A turn of `per_round` episodes
  that straddles the end of a series simply continues from its first episode.
  Without an end of its own, a "series end" visit (criteria item amount `all`)
  stops at a boundary: the moment every series has completed a pass of its
  episodes since the previous boundary (so it lasts as long as the longest
  series' pass, the shorter ones looping meanwhile).
- `drop`: that series leaves the rounds for good and the others continue; once
  every series is exhausted the block follows its usual end-of-list behavior: a
  dynamic block loops (every series starts over, round 0), while a `count`
  visit of a criteria item in a manual block ends there instead of rolling
  into the next cycle.

The walk is the series cursor above with one more persisted column,
`epg_entry.series_round`; for an interleaved walk `series_cycle` is the pass
of that episode's own series (for `drop` it is the same for every series, the
global cycle). Per-series pass state, the boundary and the position in the
rounds are all rebuilt by replaying the persisted rows, so regeneration is
idempotent and no extra column or migration was needed: rows written before
this meaning (one global cycle for every series) read correctly, each series
taking its own pass off them. A reset moves the cursor origin so everything
restarts, and the seed mixes the channel id so two channels differ. A catalog
change mid-run behaves sensibly: a series gained joins when its turn in the
round comes up (from its first episode, and it must complete a pass before the
next boundary), a removed one leaves with no hole. Switching a block between
`restart` and `drop` mid-run keeps the cursor; going to `drop` may start the
rotation over once.

A dynamic block's order is `block.playback_order` — `title` (the default and
exactly the old behavior), `release_date`, `random`, `shuffle` or `interleaved`
(issue #102) — with `interleave_spec` (JSON) and a `playback_seed` (migrations
`0013`, `0029`; PUT is a full replace, omitted means `title`). Every order but
`title` is walked as one stand-in criteria item, exactly like a criteria block
item: `title` plays the resolved `block_item` rows, the others load their own
matches and air until the slot ends. Since a dynamic block's `block_item` rows
are recreated on every catalog change, its cursor is `block_cursor` (per
channel and block) and its entries record the block id in
`source_block_item_id`. The seed is kept while the order is the same, so a
regeneration resumes the same sequence, and it mixes the channel id, so two
channels never shuffle alike. Changing the order (or `per_round`/`series_order`)
restarts the cursor.

## Scheduling model

Loosely inspired by ErsatzTV's Block Schedule + filler model
(PreRoll/MidRoll/PostRoll/Tail/Fallback), chosen over Tunarr's flat time
slots for finer control (a commercial only mid-block, a bumper only at the
edges):

- **`block`**: a reusable programming template — an ordered list of
  `catalog_item`s (`block_item`). A block's `block_item` rows are either
  curated by hand (the default, via `POST`/`PUT`/`DELETE /blocks/{id}/items`) or,
  when `block.collection_id` is set (a collection, see "Collections"), owned
  entirely by
  `catalogsync.ResolveDynamicBlocks`: it recomputes that block's membership
  against the current `catalog_item` table every time the catalog changes,
  sharing the matching query with `ResolveDynamicPools` (one evaluation per
  distinct collection). This is how a real 24/7 genre channel (e.g.
  "Comedy Movies") gets built: a dynamic `block` scheduled via one
  `schedule_item`, not a `filler_pool`/`filler_rule` misuse — `filler_pool`
  stays reserved for commercials/bumpers/station IDs (see below). The
  materializer treats a dynamic block differently from a manual one: instead
  of airing its item list once per `schedule_item` occurrence, it loops
  that list continuously (`internal/materializer/expand.go`'s
  `fillLoopingBlock`) to fill the entire window up to the next occurrence
  (or the materialized window's end for the last one), and every entry it
  emits is tagged `kind=program` — real programming, not filler. The block
  and channel filler rules apply per program, exactly as for a continuous
  item (see "Continuous schedule items and grid stability"): preroll before
  each program, postroll after it, midroll between two consecutive programs,
  tail for what is left before the next occurrence. A manual block plays its
  list per its own `list_mode` (see "Manual block list mode").
- **Series block items** (migration `0010`): `block_item.item_type` is
  `catalog` (one catalog item, as before, optional `trim_duration_ms`) or
  `series`. Type-specific data lives in the JSON `spec` column, so further
  kinds add a type value and a spec shape. A series spec is
  `{media_source_id, series_external_id?, series_title?, playback_order, seed,
  slice, slice_seed}`: the series is its media source plus external id (else
  title), `seed` makes shuffle/random reproducible and is never served, `slice`
  optionally narrows the series (below) and `slice_seed` makes its random
  sample reproducible (never served either). Series items live in
  manual blocks only (filler pools keep single items or criteria) and take no
  trim: every episode plays in full. `GET /series?q=` lists the catalog's
  series with episode count and total duration; block item responses carry a
  `series` view with the live episode count and total duration.
  **Slice.** `slice` defines the series' episode universe for the item: the
  materializer re-resolves it on every run, so it tracks the library, and the
  cursor, `amount`, `playback_order`, midroll and the rest all work on the
  sliced set exactly as on a whole series. An item without a slice airs the
  whole series, as before. Operators (all optional; none set means no slice):
  `seasons` (list) and/or `season_from`..`season_to` (a union), `exclude_specials`
  (drops season 0; superseded by the block's `include_specials`, see "Season 0
  (specials)" below, and still accepted), `episode_from`..
  `episode_to`, `skip_first`, `skip_last`, `first` or `last` (never both),
  `sample`. They apply in that order, each to what the previous left: season
  filter, episode range, skip, first/last, sample. Season and episode
  ranges are inclusive and go by real number, so gaps in the library's numbering
  are simply absent; a multi-episode file (`episode_number_end`) is in an episode
  range when any of its numbers is. `skip_*`, `first`, `last` and `sample` count
  files by position in the series' original order (season, episode), so a
  multi-episode file counts once. Episodes without a season number fall out of
  any season filter, and those without an episode number out of an episode
  range; they only take part in position-based operators. `sample` keeps N
  episodes chosen by a hash of (`slice_seed`, episode id), kept in original
  order: stable for a seed and barely moved by library changes; the seed is
  created with the slice and kept while the slice keeps sampling. Dynamic blocks
  and criteria items have no slice (they query `season_number`/`episode_number`).
  Validation (400): negative numbers, a non-positive count, a reversed range,
  `first` with `last`, `exclude_specials` with season 0 listed (or a range
  ending at 0 alone; a slice-internal contradiction, unrelated to the block option), and a slice that currently matches no episode. An empty
  slice object is no slice. `PUT /blocks/{id}/items/{item_id}` with `slice`
  replaces it (`{}` clears it); a changed slice restarts the item's cursor like
  a playback order change. `POST /series/slice-preview` (and MCP
  `preview_series_slice`) counts what a slice would leave
  (`episode_count`, `total_duration_ms`, `series_episode_count`) without storing
  anything; block item views carry the sliced count and duration. If a library
  change leaves the slice with no episode, the item is skipped like an empty
  series.
  A series item airs, per visit of the block rotation, the amount in its
  spec `amount` `{mode, count?}`: `count` (N >= 1 episodes; 1 for an item
  created without an amount), `slot_end` (until its boundary, the next
  schedule occurrence reserving postroll or the end of the window, cycle after
  cycle, so items after it in the block never air; in a continuous block the
  rotation stays on it) or `series_end` (the rest of the playback cycle in
  progress, then the rotation moves on and the next visit opens a new cycle; a
  `random` series, which has no cycles, airs as many episodes as it holds).
  The cursor is shared across visits, so one-episode visits walk the series
  across rotation loops, days and regenerations. The end of the slot always
  wins: an episode that would not end before it is not scheduled, the visit
  ends there and the block's remaining items do not air; a visit cut that way
  starts afresh (full amount) at the next gap. In a manual block a
  `count`/`series_end` visit hands over to the next item, and when the list
  ends it plays once or starts again per the block's `list_mode`. Midroll
  applies to episodes exactly as to catalog items (series items, criteria
  items and interleaved dynamic blocks alike, through the same
  `pickFiller`): one break between two consecutive episodes of a visit and
  one between a visit and the next item of the block (never after the block's
  last item), subject to the same fit rule (a break that fits is aired even if
  the next episode then does not, and the slot end always wins). Breaks carry
  no `source_block_item_id`, so the cursor rebuild never counts them as
  episodes. In a looping block (continuous item, dynamic block) a
  break goes between any two consecutive programs, episodes and plain catalog
  items alike, including across two visits. In a continuous block the entry completing a visit
  stores the unwrapped next rotation index and the others the item's own, and
  a regeneration whose kept head ends mid-visit counts the trailing adjacent
  entries still carrying the item's index to resume it. A spec without
  `amount` (stored before it existed; migration `0011` writes `slot_end` into
  them) is `slot_end`.
  Orders: `original` is season then episode
  (specials first, missing numbers last), `release_date` is premiere date
  (missing last), `random` picks any episode each time, `shuffle` plays every
  episode once per cycle in an order that is a hash of (seed, cycle, episode
  id) with the seed mixed with the channel id (two channels never shuffle
  alike); a new cycle never opens with the previous cycle's last episode. An
  episode a catalog sync adds mid-cycle is unplayed, so it takes its hashed
  place among the ones still to play; a removed one just leaves.
  No cursor is stored apart from `series_cursor.origin_ms` (per channel and
  item; moved to "now" by `reset-epg` and by a playback order change). Each
  `epg_entry` a series item produces records `source_block_item_id` and
  `series_cycle`, and every run rebuilds the cursor (cycle, episodes played in
  it, count) from those rows since the origin, so grid regeneration continues
  where it stopped. A slot that already started keeps the episode its row
  holds even if the catalog changed; later slots are recomputed. Slots that end
  before the origin (an occurrence under way when the item was added) are
  skipped without using up episodes. One cap (`maxSeriesEntries`, 20000)
  bounds the entries a single expansion emits; slots skipped before the origin
  and entries already over (before "now") do not count towards it (they have a
  separate, larger bound), hitting it logs a warning with the block item and
  channel ids. Series steps are O(1) (counters, not scans of the series), so a
  capped expansion of 1-second episodes completes in about a second.
- **`filler_pool`**: a named collection of filler content (commercials,
  bumpers, station IDs). **`filler_rule`** scopes a filler kind
  (preroll/midroll/postroll/tail/fallback) to a channel or a single block,
  drawing from a pool with a selection strategy (sequential/random/weighted);
  tail and fallback rules also carry `max_fit_attempts` (see "Block tail and
  the fill cascade").
  A pool's `filler_pool_item` rows are either curated by hand (the default,
  via `POST`/`PUT`/`DELETE /filler-pools/{id}/items`) or, when `filler_pool.collection_id`
  is set (a collection, see "Collections"), owned entirely by
  `catalogsync.ResolveDynamicPools`: it recomputes that pool's membership
  against the current `catalog_item` table every time the catalog changes
  (both push's `catalog-sync` handler and pull's `SyncOne` call it right after
  their own upserts/deletes), so a "genre X" pool always reflects whatever
  currently matches, with no manual re-run. A channel that should just be a
  24/7 loop of one genre needs no `block`/schedule at all — a dynamic
  `filler_pool` plus one `filler_rule` (`kind=fallback`, `scope_type=channel`)
  is enough, since `internal/materializer/expand.go`'s `fillFallbackGap`
  already drains a channel's fallback pool continuously to fill any window
  with no schedule covering it.
- **`schedule_item` target** (issue #100): an item names a `block_id` or a
  `collection_id`, never both and never neither (a table CHECK). A collection
  item is the shortest way to say "this rule, 24/7, in this order": it carries
  its own `playback_order` (the orders a criteria block item accepts) and needs
  no block, no block item and no wrapper of any kind. The materializer walks it
  as a single criteria item whose id is the schedule item's own, so
  `epg_entry.source_block_item_id` holds that id and the existing rebuild of a
  walk from its own rows works unchanged; its cursor lives in
  `schedule_item_cursor` (per channel and item — `series_cursor` and
  `block_cursor` both key on a block or a block item, with a foreign key to it).
  A reset or an order change moves that origin to "now", like every other walk.
  In `catalog_item` terms it airs "all matches" per visit, which for the single
  item of the loop is the whole collection, over and over.

- **`channel.loop`** (issue #103): `POST /channels` (and `PUT /channels/{id}` on
  a channel with no schedule) accepts `loop: {collection_id, playback_order}`
  and creates the supporting `schedule` (named `Loop · <channel>`) plus its one
  continuous collection item in the same request, answering
  `{channel, schedule_id, schedule_item_id}` so nothing is hidden and the author
  can graduate to a full schedule by editing what was created. A repeated
  identical create is a no-op that answers the same ids; a channel that already
  has a schedule is refused with a 409 instead of being silently replaced.

- **`schedule`** / **`schedule_item`** / **`channel.schedule_id`** (issue #7,
  the ErsatzTV Schedule/Playout split — see
  `docs/competitive-analysis/ersatztv.md` section 4): `schedule` is a
  reusable, named group of rules, not tied to any channel. `schedule_item` is
  one rule inside it — when a block airs, either weekly recurring
  (`days_of_week` + `start_time_local`, resolved against `channel.timezone`)
  or a one-off absolute `start_at` (this is `schedule_entry`'s old shape,
  before this split, minus the `channel_id` it used to carry directly).
  A weekly item holds a *list* of weekdays (0 = Sunday .. 6, at least one, no
  duplicates, stored sorted as one comma-separated column and served as a JSON
  array) and one start time: "Thursday and Friday at 22:00" or "every day at
  18:00" is one row, one write and one grid reset. The materializer expands it
  into one occurrence per listed day, each following the usual overrun and
  boundary rules; a once or continuous item has an empty list. Items that
  already exist separately for the same block and time are not merged
  automatically.
  `channel.schedule_id` is which `schedule` (if any) drives a channel; the
  same `schedule` can be assigned to more than one channel — each one still
  materializes (and tracks) its own `epg_entry` rows independently, playing
  the "Playout" role ErsatzTV gives a separate entity (not needed here: the
  materializer's per-channel, fully-regenerated-window design already covers
  it — see "EPG materialization" below). A channel with no `schedule_id` at
  all relies solely on its channel-scoped fallback filler rule, same as
  today. The one piece of per-`Run()` bookkeeping the materializer keeps
  in-memory (`fillLoopingBlock`'s dynamic-block rotation cursor,
  `internal/materializer/expand.go`) is keyed by `channelID + "|" + blockID`,
  not just `blockID`, specifically so two channels sharing one `schedule`
  (and therefore one dynamic block) don't silently share loop position —
  the concrete instance of ErsatzTV's "channels may not have the same
  content playing at the same time" guarantee this project actually needed.

### Programming plan (export and import)

`GET /programming-plan` exports the whole grid as one deterministic JSON
document and `POST /programming-plan` applies one back (issue #101). This is the
answer to "the grid is a dependency graph, so authoring it object by object is
the reason a migration becomes a script nobody can reuse".

- The document carries no numeric ids, no credentials and no paths: a
  collection, block or schedule is referenced by name, a catalog item by media
  source name + external id. Two exports of the same instance are byte
  identical (collections by name, blocks by name, schedules by name, channels by
  number).
- An import resolves references in dependency order (collections → blocks →
  schedules → channels), matches objects by name (channels by number) and is
  idempotent: importing the same document twice changes nothing. `dry_run=true`
  returns the full report (`created`, `updated`, `skipped`, `rejected` with the
  reason) without writing. `on_conflict=update|skip|reject` decides what happens
  to an object that already exists; the default is `update`.
- A rejected object rejects the objects that depend on it, with the missing name
  in the reason, and the HTTP status tells the caller (`422` when anything was
  rejected). A dry run resolves the dependencies it would have created, so the
  report of a fresh instance is complete instead of a cascade of "not in the
  plan".
- The item lists of a block or schedule are a full replace, like their REST
  writing is: an import deletes the rows and writes the document's. Materialized
  rows that point at a replaced schedule item are detached first (`epg_entry`
  has no `ON DELETE` action), and the affected channels are regenerated at the
  end when anything was written.
- Filler pools, rules and break templates are NOT part of the plan yet: they
  hang off blocks by id and their own scoping is a second graph. A plan exports
  and imports programming only.

None of the above is ever read directly by a client. It's authoring input to
EPG materialization.

## EPG materialization

A channel's assigned `schedule` (its `schedule_item` rows) + `block`/
`block_item` + `filler_rule`/`filler_pool_item` are expanded asynchronously
into flat `epg_entry` rows — concrete
`catalog_item` + `start_at`/`end_at` pairs — by a background process:

- Triggered immediately on any write to a scheduling table (schedule
  change → regenerate affected channels).
- Rolled forward on a timer so a window always stays materialized ahead of
  "now", independent of writes. The window is 48h by default and configurable
  with `PLAYHEADTV_EPG_WINDOW` (a Go duration such as `72h`, between 1h and
  720h; an invalid value is logged and ignored). A hosted deployment may want
  it longer: it is how long a client can play with no backend (see "Client
  offline cache"). The materializer horizon and `GET /epg/meta`'s `window_ms`
  both come from it.
- Implemented as an in-process goroutine/queue in the Go server — not a
  Cloudflare Cron/Queue, since the backend is a normal long-running process
  either way (self-hosted or cloud-sync), not a Worker.

This is the one non-trivial computation in the whole backend, and it never
runs on a request path. A run is bounded by a generous overall timeout and each
channel by its own 30 s budget; a channel's rows are rewritten (delete and
inserts) in one transaction, so a timeout or failure leaves its previous
materialization intact.

### Overrun policy (issue #10)

A `schedule_item` occurrence's nominal start is a hard boundary the
*previous* occurrence's content is never allowed to push past — the
"delay the next occurrence" behavior this project started with was replaced
by "stop before end": a manual block's program items (and any `midroll`
between them) that don't fit before the next occurrence's nominal start are
dropped, in playback order, down to zero items if even the first one alone
doesn't fit — there's no client-side support for trimming a single item
mid-playback, so dropping whole trailing items is the only lever available.
`postroll` (the block's sign-off) always plays in full and is reserved for
up front: its duration is picked before the item loop runs, so items stop
early enough to leave it room, rather than being computed after and
potentially pushed past the boundary itself. This only applies when a real
next occurrence exists in the current materialization window — the window's
own end (`windowEnd`) is just how far this particular run happened to
compute ahead to, not a commitment the schedule made, so the last occurrence
in a window plays out in full exactly as before (the next `Run()` picks up
the continuation). A dynamic (looping) block never had this problem to begin
with — `fillLoopingBlock` already stops exactly at the boundary by
construction (a program unit that does not fit whole before it is not aired).

### Season 0 (specials) of a block (migration `0023`)

`block.include_specials` (REST/MCP `include_specials`, default `false`) decides
whether the episodes of season 0 of a series can air from the block. Off, they
are left out of everything season-based the block selects: a dynamic block's
matches (title and interleaved orders), its criteria items and its series items.
Only an episode of a series with `season_number = 0` is concerned: movies, items
without a series (even an episode with season 0 and no series title) and
episodes with no season number are never affected. A manual block's plain
catalog items are explicit picks and are not filtered.

- **Where it is applied.** The materializer owns the filter: `loadSeriesRuns`
  passes the option to `series.Episodes` / `blockitem.Episodes` (series items,
  criteria items, interleaved dynamic walks), and `loadChannelInput` drops the
  special `block_item` rows of a dynamic block played by title (those rows hold
  the block's resolved membership, which still lists the specials, as does
  `GET /blocks/{id}/items`). The filter runs before a series slice, so `first 5`
  counts the remaining episodes.
- **Existing blocks.** The migration column default turns the option off for
  every existing block: their specials leave the guide at the next
  regeneration. Past entries already on air are not rewritten.
- **Cursors.** Changing the option keeps every cursor (`series_cursor`,
  `block_cursor`), like a catalog change does: the set of episodes grows or
  shrinks and the walk continues from its persisted position; an episode
  that left the set is simply not found any more. (A slice change, by contrast,
  restarts the item's cursor.) The write triggers a regeneration. On
  `PUT /blocks/{id}` an omitted `include_specials` keeps the stored value.
- **`exclude_specials`.** The series slice flag is superseded by the block
  option (specials are out by default). It is still accepted and still drops
  season 0 when the block includes specials; the two never conflict.
- **Open question: an explicit season 0.** A slice that names season 0
  (`seasons` containing 0, a `season_from` of 0, or a `season_to` of 0 with no
  higher start) is honored even when the block does not include specials: the
  explicit selection wins over the default (`series.NamesSeasonZero`). A range
  that only happens to cover 0 (`season_to: 3` alone) does not name it, so
  season 0 stays out. This is the implemented choice, flagged for the maintainer
  to confirm; the alternative is to make the block option an absolute gate.
- **Counts.** Block item views (`series.episode_count`, criteria
  `match_count`) and `POST /series/slice-preview` (optional `include_specials`,
  default off) report what the block would air. The query previews
  (`POST /catalog-items/preview-criteria`, `GET /collections/{id}/preview`, which
  the dynamic block editor shows) describe a query, not a block, and still count
  season 0.

### Manual block list mode (migration `0015`)

`block.list_mode` says what a manual block does when its item list ends:
`repeat` (the default for a new block) starts again from the first item,
`once` airs the list a single time. It describes the content, so it applies the
same whether the block is scheduled with a time or as a continuous item. A
dynamic block has no mode (it always loops its matches by its own rules);
the API serves `list_mode` on manual blocks only and rejects it on a dynamic
one with a 400, as it does any value other than `repeat`/`once`. On
`PUT /blocks/{id}` an omitted `list_mode` keeps the stored one (the rest of the
PUT stays a full replace); the web UI and MCP `create_block`/`update_block`
expose it the same way.

- **Timed item** (weekly or once): with `repeat`, every lap of the list starts
  from the first item and the block keeps airing until the next occurrence (the
  window's end when there is none). The overrun policy is unchanged: an item
  that would not end before the next occurrence (minus a reserved postroll) is
  not scheduled, which ends the block, and the `tail` filler takes what is left.
  Midroll goes between consecutive items, including from the last item of a lap
  to the first of the next. A `repeat` block that runs up to the window's end
  has no postroll yet (it has not ended). Each occurrence restarts the list at
  its first item, so a regenerated occurrence is identical and no rotation
  position is stored for it; series items keep their own persisted cursors
  across laps, occurrences and runs. `once` is the behavior from before the
  setting: one pass, then tail/fallback.
- **Continuous item**: the rotation already wrapped indefinitely, which is now
  `repeat`. With `once` it airs the list a single time and then the channel's
  fallback filler takes over; the rotation position persisted in
  `epg_entry.rotation_next` is the unwrapped index, so `len(items)` means "the
  list has been played" and survives runs and restarts (`reset-epg` plays it
  again).
- **Series items** inside a repeating list behave as in a looping block: a
  `count`/`series_end` visit airs its amount and the walk moves on to the next
  item, a `slot_end` visit stays on the item until the boundary (items after it
  never air), and the cursor carries across laps. `maxSeriesEntries` still
  bounds what one item emits.
- **Guards.** A lap that airs nothing (an empty block, only empty series, items
  without a positive duration) ends the block instead of looping, items with a
  non-positive duration are skipped in a repeating list, and one occurrence
  emits at most `maxRepeatEntries` (20000) entries. A scheduled `repeat` block
  none of whose items can air raises the `repeat-empty` epg warning
  (`GET /channels/{id}/epg-warnings`).
- **Migration.** Every existing block becomes `repeat`. The migration also
  fills `block_list_mode_review` with the manual blocks that have items and are
  referenced by a weekly or once schedule item (their grid may now differ: a
  list shorter than its slot used to end and now starts over). They are logged
  at startup and served by the read-only `GET /blocks/list-mode-review`
  (`[{block_id, block_name, schedules: [{schedule_id, schedule_name}]}]`, only
  blocks still `repeat` and still timed). Writing `list_mode` on the block (or
  deleting it, or making it dynamic) settles its review; the web UI shows a
  notice on the blocks list and on the block editor with "Play once" / "Keep
  repeating" buttons. A rename does not settle it.

### Continuous schedule items and grid stability

A `schedule_item` with `recurrence_type = continuous` has no time: it is the
base layer of a channel, airing in every span the timed items leave (and
rotating through its block, whole programs only, with a residual too short for
the next program handed to the channel's fallback rule). At most one per
schedule, enforced by the API (`409` otherwise). A timed item still starts at
its nominal time; the continuous item resumes right after it where its
rotation left off.

The channel and block filler rules apply to a continuous item (and, the same
way, to a dynamic block scheduled with a timed item) around each *program*, a
catalog item or an episode of a series/criteria item:

    [preroll] program [postroll] [midroll] [preroll] program [postroll] ...

- **preroll** before each program, **postroll** after it, **midroll** between
  two consecutive programs (after the postroll, before the next preroll), none
  before the first program of a span or after the last one.
- A program is aired as a *unit* with its preroll and postroll (and the
  midroll owed before it) only if the whole unit ends before the span does, so
  no filler is left orphaned at a boundary.
- **tail** only covers what is left of the span when whole units no longer fit
  (before the next timed item or the window's end); the channel's fallback rule
  then fills whatever the tail leaves. A manual block under a timed item is
  unchanged.
- Fillers are whole pool items chosen by the existing rule resolution (block
  scope over channel scope) and selection strategies. There is no limit on how
  much filler may sit between two programs beyond what the rules impose;
  `GET /channels/{id}/epg-warnings` reports `filler-ratio` when the longest
  items of the preroll, midroll and postroll pools add up to more than 50% of the
  shortest program of the block.
- Guards: a program or filler with a non-positive duration airs nothing, and
  one fill emits at most `maxLoopEntries` (50000) entries, so near-zero lengths
  cannot spin; `maxSeriesEntries` still bounds series and the channel/run
  timeouts still apply.

Changed behavior: a dynamic block under a timed item used to air the preroll
once at the top of the occurrence, no postroll/tail, and a midroll only between
series episodes. Already-materialized grids keep that shape until the
materializer next regenerates the channel (the normal run, or any schedule/
filler edit); there is no data migration.

The persisted position after a program or filler is on the entries
(`rotation_next`), and a kept head that ends in the middle of a unit is resumed
exactly: the last kept entry's kind (preroll, program, postroll, midroll) says
which part of the unit is still owed.

A channel whose schedule has a continuous item is not fully regenerated on
every run. The materializer keeps the rows already materialized that start
before `now + freeze margin` (3h by default, `PLAYHEADTV_EPG_FREEZE_MARGIN`
overrides it, exposed as `freeze_margin_ms` in `GET /epg/meta`) and only
recomputes what starts after, continuing from the end of the last kept row.
The rotation position is persisted on the entries themselves
(`epg_entry.rotation_next`, the block-item index the next program takes), so
it survives runs and restarts and never depends on in-memory state. Channels
with only timed items keep the full-window regeneration described above.
A gap inside the kept head (nothing on air, or a hole between two kept rows)
is not preserved: the run drops the kept rows from the hole onwards and
regenerates them from the current schedule.

`POST /channels/{id}/reset-epg` force-resets one channel: it discards the
materialized future and the entry on the air, clears the persisted rotation
(it restarts from the first block item) and regenerates synchronously. It is
the way a change inside the freeze margin takes effect early. The web UI
exposes it on the channel list behind a confirmation dialog.

Schedule edits trigger the same reset automatically, for every channel they
affect: creating, updating or deleting a schedule item resets every channel
using its schedule, and assigning, swapping or clearing a channel's schedule
resets that channel (REST and MCP alike). The reset runs synchronously inside
the request, like the endpoint above, and waits at most for a materializer run
already in progress (they share one mutex). Catalog-only changes (sync,
catalog edits) never reset; they keep the freeze margin.

### Base schedule (migration `0024`)

A channel may reference a second schedule, `channel.base_schedule_id`
(nullable, next to `schedule_id`, never the same schedule: validated by the
REST and MCP writes and by a `CHECK`). Exactly two layers exist:

- the **pinned** layer is the channel's `schedule_id`, expanded exactly as
  described above (timed items, overrun policy, its own preroll/midroll/
  postroll/tail);
- the **base** layer is the base schedule, which plays real programming
  (its own blocks, its own filler chain, any recurrence including a
  continuous item) in every gap the pinned layer leaves, in place of the
  continuous item and plain fallback filler.

The pinned layer always wins. The base is expanded over the whole window as
if it ran all the time underneath, by the same code as any schedule, with a
selection state of its own, and the pinned layer then takes from it what falls
in the spans it leaves empty (the spans that used to go to
`fillFallbackGap`). Nothing about the base is stored per channel: what airs at
an instant is a function of the stored configuration, so a full-window
regeneration is deterministic and the base continues, when a pinned occurrence
(with its postroll and tail) ends, at the position it would have had anyway,
not where it left off.

Taking a span from the base (`baseGapFiller` in `expand.go`):

- entries inside the span are used as they are;
- a program that began before the span continues from where it is: its entry
  starts with the span and `item_offset_ms` says how far into the file;
- a program running past the span's end is cut there, so the pinned
  occurrence starts on time (the entry is shorter than the item, like a
  segment of a split item); the last span of the window is not cut;
- a program with less than a minute of it left in the span is dropped, and
  so is any preroll/midroll/postroll/tail that is not wholly inside the span;
- what the drops leave empty goes to the channel's fallback rule (whole
  fillers that fit), and the base layer's own holes (a timed base schedule
  with no continuous item) are filled by that rule too. Filler is thus only
  what the base does not cover.

The configuration warnings follow the same rule: with a base schedule a slot's
leftover that the base airs is not an `unfilled-slot`, and only a span the base
itself leaves empty before the next pinned item is reported (a channel fallback
rule silences it, as without a base). The channel's "no fallback filler" notice
then speaks of time the base schedule does not cover.

With no base schedule nothing changes. With one, the pinned schedule's own
continuous item does not air in the gaps (the base owns them), and the
continuous item whose rotation is persisted for grid stability is the base
schedule's, so the freeze margin applies to the base layer. Known limits: the
persisted rotation position is that of the last base entry before the frozen
head's end, so when that end falls inside a pinned occurrence the base
programs hidden under it are walked again from there; a series or criteria
item walked by both layers (the same block in both schedules) shares its
cursor, which is rebuilt from the entries that aired.

Authoring: the channel's Deco tab in the web UI has an optional "Base
schedule" select; the MCP tools are
`assign_channel_base_schedule` and the `base_schedule_id` field of
`update_channel`. The client reads only the materialized guide, where base
entries are ordinary `program`/`preroll`/... entries, so it needs no change.

### Watermark (migration `0025`)

A channel and a block may each carry a watermark: a small image the client
draws over the player. It is a client-rendered overlay (the backend never
transcodes), so a watermark is configuration the backend stores and resolves
and the client paints.

**Settings.** One `watermark` row per owner (`owner_type` channel or block;
no row means the default of the level). Every owner has a `mode`:

- `inherit`: a block uses its channel's watermark; for a channel it means none.
- `disable`: no watermark. For a block, none while its programs, and the filler
  that decorates them, are on air.
- `override`: the owner's own image and options.

A channel is therefore "on" only in `override` with an image; the web UI shows
it as Off/On. Options (same at both levels): `position` (`top_left`,
`top_right`, `bottom_left`, `bottom_right`; default `bottom_right`),
`opacity_pct` (0-100; default 70) and `size_pct` (percent of the player's
height, 2-40; default 8; the image keeps its aspect ratio and is never wider
than 40% of the width).

**Image.** Uploaded by the web UI or the MCP tool, inside the JSON body as
base64 (`image_base64`), PNG, JPEG or WebP only, at most 1 MiB decoded. The
type is decided by sniffing the bytes (`http.DetectContentType`), never by an
extension or a declared type, so an SVG (a document that can carry script) or
anything else is rejected (`415`, too large `413`). The bytes are stored in
the database. Every upload gets a fresh `image_id`, the immutable identity of
the bytes and of their URL, `GET /watermarks/{image_id}/image`, served with its
content type, `Cache-Control: private, max-age=31536000, immutable`, an `ETag`,
`nosniff` and a `default-src 'none'; sandbox` CSP. A device token may read it
(like `/epg` and `/channels`). Deleting a channel or a block deletes its row
and with it the image; `DELETE .../watermark` clears one explicitly.

**Resolution (per guide entry, in the backend).** `GET /epg` and the
epg-preview carry on each entry that has one a `watermark` object
`{image_id, position, opacity_pct, size_pct}`, omitted when none applies (so a
guide with no watermark configured is byte-identical to before). The entry's
block is that of the schedule item that produced it
(`epg_entry.source_schedule_item_id`):

1. a block in `override` with an image: its own;
2. a block in `disable`: none;
3. a block in `inherit` (or with no row), and an entry with no schedule item
   (a channel fallback): the channel's, if it is on.

Filler (preroll, midroll, postroll, tail) carries the source schedule item of
the program it decorates, so it follows that block whatever the scope of its
filler rule; only an entry with no source (a channel fallback in a gap) takes
the channel's directly. A block `override` with no image yet resolves to none.
A grouped tail carries the watermark of its first clip. The base schedule's
entries are ordinary entries of the base schedule's own items, so they resolve
by their block like any other.

**Why per entry, not a channel plus block table.** The client already holds
exactly the on-air entry and reads only the guide; resolving server-side
keeps the rule in one place, adds one optional object to an array it already
downloads, needs no second request and no new cache table, and the entry
JSON in the offline cache simply carries it (`scheduleCacheSchemaVersion` is
4, moved for the channel logo). The cost is a little repeated JSON per entry, small next to the titles
and art tags.

**Compatibility.** The `watermark` field is optional on every entry. An older
client ignores it; a newer client against an older backend sees none and draws
nothing. Neither can fail, so `buildinfo.APIVersion` is not bumped.

**Client.** `WatermarkOverlay` (`client/lib/widgets/watermark_overlay.dart`)
is a child of the player's `Stack` above the video and below the loading
spinner, the OSD, the EPG and the dimmer. It listens to the player's
`_watermarkNotifier`, a `ValueNotifier` set from the on-air entry's
`program.watermark` wherever `_currentIndex` takes a value
(`_syncWatermark`), never `setState`, so a program change never rebuilds the
video widgets. It is pointer-transparent and never focusable, so no 6-button
flow is affected. Its margin is scaled with `UiScale` (`context.ui`) and its
size is relative to the player's height. It is not shown on the gap slate
(the notifier is cleared when a hold starts), not while a channel loads
(cleared on a channel switch) and not on the mosaic previews (the mosaic does
not use the widget). The image comes from the art cache, keyed by its URL: the
syncer prefetches the images of the cached entries (with the device token, a
few small files) after each refresh, and the overlay falls back to one
download when it is not on disk; a failed download, a missing image or an
undecodable file means no watermark, never an error. A settings change reaches
the player with the next guide refresh; a refresh that leaves the on-air
program in place does not redraw it, so the new settings show from the next
program on.

**Authoring.** Channel Deco tab: a "Watermark" section (Off/On, then image,
corner, opacity and size only when On). Block editor: a "Watermark" select
(Inherit the channel's / No watermark / Use its own) that reveals the same
controls only for "Use its own". Each section saves with its own button. MCP:
`get_channel_watermark`, `set_channel_watermark`, `clear_channel_watermark` and
the same three for a block (`get_block_watermark`, ...); the image goes in
`image_base64`. The watermark is deliberately its own resource, not fields of
`PUT /channels/{id}` and `PUT /blocks/{id}` (full replaces that would clear it
on every unrelated edit and carry a multi-hundred-KiB body).

Known limits: one watermark image per owner (no per-program, no animated
image); the size is relative to the player area, not to the letterboxed
picture inside it; changing a watermark does not reset the grid, so it takes
effect at the next guide refresh and program change.

### Channel logo (migration `0027`)

A channel may carry a logo: the image the playback client draws **in place of
the channel name**. Two places use it — the channel column of the guide and the
OSD header — and that is the whole point of the feature: a self-hosted
installation shows its own channel brands instead of plain text. It is not the
watermark's sibling twice over: there is no position, opacity or size, because
the client lays the logo out in the layout it already has.

**Storage and identity.** Three columns on `channel` (`logo_image_id`,
`logo_content_type`, `logo_image`), not a table of their own: a logo can never
outlive its channel, deleting the channel deletes it, and no join is added to
the channel read every client does. `channelColumns` (in `internal/api`,
`internal/materializer` and `internal/mcpserver`) selects `logo_image_id` and
never the bytes, so listing channels stays cheap.

**Routes.** `PUT /channels/{id}/logo` takes `{"image_base64": "..."}` (PNG,
JPEG or WebP, at most 1 MiB decoded, sniffed by content — the same rules and
the same helpers as a watermark, `SniffImage` in `internal/api/image.go`; SVG
is refused as a document that can carry script); `image_base64` is required,
unlike a watermark's, since an upload without an image would only re-stamp the
row. `GET /channels/{id}/logo` returns
`{channel_id, image_id, content_type, updated_at}`; `DELETE /channels/{id}/logo`
clears it; `GET /channel-logos/{image_id}/image` serves the bytes with
`Cache-Control: private, max-age=31536000, immutable`, an `ETag`, `nosniff`, a
`default-src 'none'; sandbox` CSP, and accepts a device token (that is how the
playback client reads it). Uploading and clearing are authoring, so they need
an app_user session, exactly like the channel itself.

**Cache-busting is the id.** Every upload writes a fresh `logo_image_id`, which
`GET /channels` exposes as `logo_image_id`, and that is what the client turns
into the image URL. The art cache never revalidates a file it already has, so a
URL that changed with the image is what makes replacing a logo visible at all;
the previous id stops resolving (`404`).

**Client.** `PlayheadChannel.logoImageId` carries it; `PlayheadApi.channelLogoUrl`
builds the absolute URL; the syncer prefetches one file per channel into the art
cache **with the device token** next to the posters and the watermarks, so a
channel watched offline still draws its logo. `ChannelLogo`
(`client/lib/widgets/channel_logo.dart`, an `ArtImage` with the backend's
headers) is drawn in two places.

**In the guide's channel column** the row reads `number [logo] name`: a small
number (13 px, muted, fixed width), a fixed 92x44 logo slot, then the name at
15 px, clipped with an ellipsis (the row keeps the `epg-channel-name-<id>` key).
The number is deliberately smaller than the name — it is the row's address, not
its identity — and shrinking it is what buys the logo the height it needs to be
recognisable. The slot has a fixed width instead of shrinking to the image:
logos arrive in whatever aspect ratio they were uploaded in (square brand marks
next to 16:9 wordmarks), and a box that followed each one would start the name
at a different x on every row. The logo is centred inside the slot and keeps its
own ratio (`BoxFit.contain`), so a square and a wide mark take the same room and
the names line up. A logo that is not on disk yet and cannot be downloaded
leaves the slot empty; the name is drawn either way, it no longer stands in for
the logo.

**In the OSD header** the logo takes the name's place and the leading guide-art
badge is dropped, since drawing the same image twice in one line is noise. A
channel without a logo is unchanged there: guide art plus the upper-cased name.

**Web UI.** The logo is uploaded in the channel's Deco tab
(`webui/src/components/ChannelLogoSection.tsx`: preview, save, replace, remove,
PNG/JPEG/WebP up to 1 MiB) and drawn in the EPG page's channel column. Both go
through `webui/src/lib/storedImage.ts`, which fetches the bytes with the session
token and hands back an object URL — an `<img src>` cannot send that header — and
revokes it on change. Storing the id in the cached channel JSON is
why `scheduleCacheSchemaVersion` moved to 4 (an older file is recreated and
refilled by the next sync).

**Authoring.** MCP: `get_channel_logo`, `set_channel_logo` (image in
`image_base64`) and `clear_channel_logo`. There is no web UI section for it yet.

Known limits: one raster image per channel (no SVG, no animated logo, no
per-program logo); the client scales it to fit a fixed height, so a very wide
wordmark and a square badge share the same box; it is not drawn on the mosaic
tiles or in the OSD's recent-channel cards, which keep their text names.

### Deco tab

A channel's decorative behavior is authored in one place, the **Deco** tab of
the channel page (`?tab=deco`). It is a composition over existing resources,
not an entity: no table, no endpoint, no MCP tool of its own.

- **Common controls**, always visible and all optional: Base schedule (the
  channel's `base_schedule_id`, saved with `PUT /channels/{id}`), Watermark
  (`WatermarkSection`, its own resource) and Fallback filler (one
  channel-scoped `fallback` filler rule: picking a pool creates or repoints
  it, "None" deletes it). A new channel has none of the three and plays its
  schedule with an empty gap where nothing is scheduled.
- **Advanced**, collapsed unless the channel already has rules beyond the
  plain fallback: it embeds `ChannelFillerRules` (per-kind rules, scope,
  selection strategy, position, several pools, break templates) and links to
  the global Filler pools and Filler rules pages. The decision lives in
  `webui/src/lib/deco.ts` (`hasAdvancedRules`): any per-kind rule of the
  channel, any rule of a block in its schedule, several fallbacks, or a
  fallback with a non-default strategy or fit attempts. In that last case the
  simple control defers to Advanced instead of hiding options it cannot show.
- **Block overrides** stay in the block editor (watermark select, block-scoped
  rules); the tab only points at it.

The Settings tab keeps the channel fields and the pinned schedule, with a
pointer to Deco instead of the base schedule and watermark controls.

### Block tail and the fill cascade (migration `0017`)

What a slot (a timed occurrence, or the continuous item's stretch before the
next timed item) airs is decided in layers, each covering only what the one
before left:

1. **Items**: the block's own content, whole items only, never cut short (a
   manual block per its list mode, a dynamic block or continuous item looping).
2. **Tail filler**: the resolved `tail` rule (block scope over channel scope)
   fills `[end of items, next occurrence)` with `kind=tail` entries. This
   applies to manual blocks, dynamic blocks and the continuous item alike.
3. **Fallback**: the channel-scoped `fallback` rule fills whatever the tail
   left, up to the next occurrence.
4. **Gap**: with no rule that applies (or nothing in the pool short enough),
   the rest has no `epg_entry`.

A tail or fallback fill picks whole pool items. `filler_rule.max_fit_attempts`
(1 to 20, default 1, validated on write with a 400, ignored by the other
kinds) says how many candidates in a row it may reject for not fitting before
giving up on the rest of the span:

- A rejected pick is replaced by another candidate: the next ones in order for
  `sequential`, a draw without replacement for `random`/`weighted`. The fill
  keeps going with whatever fits while time is left; the attempts bound
  consecutive misses, not the number of items aired. With 1 it stops at the
  first miss.
- Skipped candidates do not use up a `sequential` rotation slot: the rotation
  advances past the item that actually aired, and a stretch in which nothing
  fits leaves it where it was. Consequence: with 1 attempt, a sequential pool
  whose next item is longer than every residual never rotates past it; raise
  the attempts to let shorter items through.
- A residual shorter than the shortest item of the pool is not attempted at
  all. The cost of attempts is bounded by the limit, and the 50000-entry cap
  per fill still applies.

Tail entries stay `kind=tail`, so the guide hides them and `group_tail=1`
groups a run of them whatever block produced it. The guide/OSD attribution cap
for a tail (30 minutes) is unrelated and unchanged. The last timed occurrence
of a manual block has no next item, so its tail runs to the end of the
materialized window; the next regeneration continues from there.

`GET /channels/{id}/epg-warnings` reports `unfilled-slot` for a block whose
slot can end with a minute or more that nothing covers (items, tail and the
channel fallback considered, a real next timed item required). It expands a
week of the stored configuration read-only to find it, and is informational:
the web UI shows it in the channel guide next to the other block warnings.

### Break templates (migration `0018`)

A `preroll`, `midroll` or `postroll` filler rule inserts one pool item at a
program boundary. A **break template** replaces that single item with an ordered
list of steps, for example `[single bumper from pool A] -> [fill 4 min from
pool B] -> [single bumper from pool C]`. The plain rules are the degenerate
one-step case and are neither migrated nor rewritten: with no template in play
the expansion is the same code path with one part per break.

Model: two tables, `break_template` (scope `channel`/`block`, `scope_id`,
`kind` in preroll/midroll/postroll, `name`, `position`) and
`break_template_step` (`position`, `step_type`, `filler_pool_id`,
`selection_strategy`, `target_ms`, `max_fit_attempts`). Rows rather than JSON on
`filler_rule` because a step references a pool (a real foreign key, so a pool in
use cannot be deleted from under a template, as for rules), the steps are
edited and ordered individually, and each step id keys its own sequential
rotation. A step is:

- `single`: one item drawn from the pool by the selection strategy, exactly as a
  plain rule does (an empty pool or a non-positive duration skips the step);
- `fill`: whole items of the pool until `target_ms` is filled, through the same
  `fitPicker` as the tail and fallback fills, so `max_fit_attempts` applies and a
  skipped candidate keeps its rotation slot. The target is a fixed length chosen
  by the author, never derived from the slot. What no candidate fits stays
  unfilled (the break is a little shorter); items are never cut short.

Limits (400 on write, REST and MCP share `internal/breaktemplate`): 1 to 10
steps, `target_ms` 1 to 3,600,000, `max_fit_attempts` 1 to 20, every pool must
exist, a name up to 100 characters. A `fill` step emits at most 1000 items per
break (`maxFillStepParts`).

**Precedence** at a boundary of kind K for a block (`resolveBreak`):

1. the block's own configuration beats the channel's: a block-scoped template or
   rule of kind K decides, and nothing channel-scoped is consulted;
2. within one scope level a template beats a plain rule of the same kind;
3. among several templates (or rules) at one level, the lowest `position` wins.

So a block rule still beats a channel template, and a block template beats both
a block rule and everything at channel scope. `tail` and `fallback` are not
boundary kinds and take no template.

Emitted entries keep the hidden kinds (`preroll`, `midroll`, `postroll`); no new
entry kind exists, so the `epg_entry.kind` constraint, the 30-minute guide
attribution, `group_tail` and the OSD behave as before. Each part is its own
entry, back to back; in the continuous and dynamic loops every part carries the
break's rotation position (the program and all parts of its postroll carry the
position after it), so a resume reads the last row.

Block length: breaks may lengthen a block; the tail still absorbs the remainder
to land on the next slot. A unit (`[mid][pre] program [post]`) airs only if all
of it, template parts included, fits before the limit; a manual block under a
timed item reserves the whole postroll break before the next occurrence, as it
does for a single postroll, and a midroll or preroll break that does not fit
stops the block's content.

Resume (grid stability, continuous items): a kept head can end between two parts
of one template break, which cannot say which steps are owed. When the last
kept row is a preroll/midroll/postroll governed by a template, the kept head
grows to the end of the contiguous run of break rows that follow it
(`extendKeptThroughBreak`), so the whole break the previous generation laid down
stays as it was and the generation resumes from a stage that owes whole breaks
only. Plain single-item breaks never need this. As with any pool, a
regeneration restarts the in-memory sequential rotations (accepted v1 limit).

Cost and bounds: the `filler-ratio` warning counts a template's worst case (sum
over steps of the longest pool item for a single step, the target for a fill step
that some pool item can fit). The 50,000-entry cap of a looping fill counts every
part, since each is an entry; it is checked per unit, so one unit can overshoot
by at most 3 breaks x 10 steps x 1000 parts. Measured on a week of one channel
with 3-minute programs, a midroll template of single + 4 min fill + single and a
20,000-item pool: about 9,000 entries in 10 ms (sequential), 13 ms (random) and
120 ms (weighted, which scans the pool per pick).

REST: `POST/GET /break-templates`, `GET/PUT/DELETE /break-templates/{id}` (a PUT
replaces the template and its steps); MCP: `create_break_template`,
`list_break_templates`; web UI: Break templates (list and step editor).
Break templates cover the boundaries between programs; the breaks inside an
item, between its segments, are the next section.

### Items played in segments (migration `0020`)

One catalog item can be played in segments: cut at some offsets, with the
midroll break of its block (or channel) between consecutive segments. Where to
cut is a separate concern (the cut-point time model); this section is the
mechanism.

Model, in `epg_entry`:

- `item_offset_ms INTEGER NOT NULL DEFAULT 0` (`>= 0`): where in the catalog
  item the entry starts playing. It plays for `end_at - start_at` from there, so
  a segment's own span is its duration and may be shorter than the item's.
  Every entry that is not a later segment has offset 0.
- `split_group TEXT` (NULL for an entry that is not a segment): the same opaque
  value on every segment of one airing of an item (the first segment's entry
  id). The breaks between segments are ordinary `midroll` entries and carry no
  group; a segment is a `program` entry like any other.

Why an explicit group and not "consecutive entries of the same item separated
only by hidden breaks": the same item can legitimately air twice in a row (a
one-item repeating block), and a window or a client cache that starts at a
later segment would have no way to tell a continuation from a new airing. The
key makes the tie survive any slicing of the rows. The offset alone is not
enough either: a later segment has offset greater than 0, but an airing the
window cut at its start looks the same.

`GET /epg` (raw and enriched, `group_tail` or not) and the MCP `preview_epg`
add both fields, omitted when they have no value (offset 0, no group), so the
payload and the `ETag` of a guide without segments are unchanged. A segment is
enriched from its item like any entry (`duration_ms` is the item's length).

Materializer (`internal/materializer/cuts.go`):

- `CutPlanner` is the seam: `PlanCuts(CutItem{CatalogItemID, DurationMs, ChannelID, BlockID}) []int64`
  returns the ordered offsets at which a break goes (none = the item plays
  whole). The default planner never cuts (the server installs `RuleSetCuts`) (`Materializer.SetCutPlanner`). The result is normalized (sorted,
  de-duplicated, nothing within `minSegmentMs` (1s) of an end or of another
  cut, at most `maxCutsPerItem` (20)), and a cut whose break resolves to
  nothing (no midroll configured, empty pool) is skipped, so a planner can be
  naive. It must be deterministic for the same item.
- `segmenter.split` turns the planned program entry into segments plus breaks:
  the first segment keeps the entry's id and fields, the others copy them with a
  fresh id, their own span and offset, and the break between two segments is
  `pickBreak` of the midroll source (plain rule or break template). Every place
  that emits a program calls it: the manual block list, the continuous and
  dynamic loops (inside the unit, between preroll and postroll) and series
  episodes (both walks).
- The space the breaks add is taken from the slot like any other break: each
  caller checks the whole span against its limit (overrun policy, unit-must-fit,
  strict series fit), and the entry caps count every part.
- Resume (continuous items): a kept head that ends between the segments of one
  item cannot say how much of the item has aired, so it grows to the end of the
  last segment of that item (`extendKeptThroughSplit`, after the break
  extension). Series cursors are rebuilt from the rows with offset 0 only, and a
  count visit counts an episode at its offset-0 row and treats later segments
  like midrolls.
- The real planner is `RuleSetCuts` (see "Cut points"); the seam is also what
  the tests plug a fixed planner into.

Client playback of a segment: `ChannelEntry.itemOffset` is the entry's
`item_offset_ms`. Every controller created for the entry seeks to the offset
plus whatever it joins in the middle of (`_createController`: a tune-in inside
the segment, or a resync after running behind), and the transition timer fires at
the segment's end (its own `end_at - start_at`), not at the item's. The
dual-controller design needs no new mechanism: seg1, the break and seg2 are three
consecutive units, so while the break plays the preload creates a second
controller on the same file, seeked to the resume point, and the normal
crossfade brings it in. The outgoing segment keeps playing the film past its cut
for the length of the 800 ms crossfade (the stream is not trimmed); that is
accepted rather than worth a stop timer. The offline cache stores both fields (`scheduleCacheSchemaVersion` 2).

Guide and OSD: the guide (`buildGuideCells`, client and web UI, same cases)
draws the segments of a group as ONE program: a cell from the first segment's
start to the last one's end, whose entry is the first segment (title, art, the
OSD's announcement key), with the later segments and the breaks between them
absorbed. "Live", focus, navigation and the OSD's progress (wall clock over the
merged span) all work on that cell. A window that holds only a later segment
shows that segment as a program of its own. Everything else, the tail cap
included, is unchanged for items without segments.

### Cut points (migration `0019`)

A break template airs at a program boundary. Cutting inside an item needs two
things: a way for an `epg_entry` to start at an offset into its item, and a
rule for where the cuts fall. This section is the second, the **cut-point time
model**: `internal/cutplan` (pure: no storage, clock or randomness) plus the
stored, authored `cut_rule_set`. The materializer reads the stored sets through
the `CutPlanner` seam (see "Wiring" below).

Rule set: scoped like a `filler_rule` (`channel` or `block`, `scope_id`, `name`,
`position`; a block's own set beats its channel's, then the lowest `position`,
see `cutplan.PickSet`). One table, `cut_rule_set`, with the rules as a JSON
`config` column (`domain.CutConfig`): they have no foreign keys and are always
read and written whole. The config is an ordered `rules` list, an optional
`default` strategy and set-wide `guards`.

Rule selection: the first rule whose selector matches the item wins. A selector
is `kinds` (movie/episode/other, empty = any), a duration range
`[min_duration_ms, max_duration_ms)` (0 = open) and `has_chapters` (unset = any;
an item has chapters when it has at least two, i.e. a boundary after 0). No
matching rule: the set's `default` strategy, or no cut at all when there is none.

Strategies (the `type` of a rule's `strategy`):

- `interval`: every `every_ms` from the start (1 minute to 24 hours);
- `percentage`: at each of `percentages` (above 0, below 100, ascending) of the
  duration;
- `offsets`: at the explicit `offsets_ms`;
- `phased`: ordered `phases`, each `every_ms` until its end, the last running to
  the end of the item. A phase cuts at its own start plus multiples of its
  cadence (so "every 40 min until the last 20 min, then every 5 min" on a
  150-minute film gives 40, 80, 120, then 135, 140, 145). Bounded phases end
  with `until_ms` (from the start) or `until_before_end_ms` (from the end), all
  of the same kind, strictly ordered, so phases cannot overlap and the order
  never depends on the item's duration;
- `chapter`: `roles` `after_intro` (the end of the item's first Jellyfin
  `Intro` media segment when it has one, see "Media segments" below; else the
  chapter boundary after the first chapter named
  intro/opening/prologue/cold open/teaser, else after the first chapter when it
  ends within 10 minutes) and `middle` (the chapter boundary
  nearest the middle, or the middle itself when none is within the tolerance),
  snapped within `snap_tolerance_ms` (5 minutes by default);
- `none`: an explicit "do not cut".

Any rule's `snap_tolerance_ms` also moves the cuts of the non-chapter
strategies onto the nearest chapter boundary within that distance, once the item
has chapters.

Guard rails (set-wide `guards`, overridable field by field in a rule's own
`guards`; 0 takes the default): `min_segment_ms` (3 minutes) between cuts and
to the edges, `min_from_start_ms` and `min_from_end_ms` (3 minutes), and
`max_breaks` (10, at most 50; the earliest are kept). Cuts are sorted,
de-duplicated and thinned in that order, so an item too short for them gets no
cut. The output is a pure function of the rules, the item's kind and duration
and its chapters and media segments: regenerating the guide never moves a break.

Fallback chain: a `chapter` strategy that finds no intro segment or chapters (or
none of its roles resolves) uses its own `fallback` (a percentage, interval, offsets, phased
or none strategy), else one built-in cut at 50%: an item is never left uncut
for lack of data unless the author chose `none`. A fallback result thinned to
nothing by the guard rails is not retried; an item that is too short stays
uncut.

Validation (400 on write, REST and MCP share `internal/cutruleset`): at most
20 rules, bounds and ordering of every strategy, stray fields for the type,
phases (1 to 5, ordered, non-overlapping, same bound kind), a selector whose
minimum is below its maximum, and a rule that an earlier rule already covers
completely ("rule 3 can never match: rule 1 ...").

Consumption contract: `cutplan.Planner` adapts a stored set to
`CutOffsets(item domain.CatalogItem, durationMs int64) []int64` (ordered offsets
into what airs, empty for none; `durationMs` is the occurrence's effective
duration after a block's trim). Chapters and media segments come from
`item.Chapters` and `item.Segments`, which the catalog list queries leave empty:
a caller that wants chapter or segment cuts loads them first, otherwise the
planner takes the no-data path.

Wiring (`internal/materializer/rule_cuts.go`, installed by `cmd/server`):
`RuleSetCuts(db)` is the `CutPlanner` backed by the stored sets. Each time the
materializer loads a channel's input it asks the planner for a snapshot
(`BeginPass`): the tenant's sets are read once (one small query, none = no
planner, so a tenant without sets pays nothing and generates exactly what it
did before), and the snapshot answers `PlanCuts` per item: `cutplan.PickSet`
chooses the set for the airing's channel and block, the catalog item's `kind`
(and its stored `chapters`, only when a rule can use them: a `chapter` strategy,
`has_chapters` or a snap tolerance; and its stored `media_segments`, only when a
rule has the `after_intro` role, `cutplan.UsesSegments`) is read once per
distinct item per snapshot, and `Plan.CutsMs` is returned. An item no set governs is not looked up at all.
A failed lookup is logged and the item is not cut; materialization never fails
on it. Chapters are persisted by the pull sync (`catalog_item.chapters`), so
chapter strategies work on a synced Jellyfin library.

`Plan.Source` says what decided the cuts: `rule` or `default` (a non-chapter
strategy), `segment` (a chapter strategy resolved a cut, at least `after_intro`,
from an Intro media segment), `chapter` (resolved from the chapters alone),
`fallback` or `builtin` (a chapter strategy found neither), `none`. The preview
and the web UI show it per sample ("via the intro segment").

Media segments (migration `0021`, `catalog_item.media_segments`): a JSON array
of `{type, start_ms, end_ms}` (types `intro`, `outro`, `recap`, `preview`,
`commercial`), exposed by `GET /catalog-items/{id}`, MCP `get_catalog_item` and
the push sync input as `segments`. NULL means never looked up, `[]` looked up
with none. The planner uses the first Intro (by start) that has a positive
length, starts inside the item and ends before its end; the cut is its end and
is not snapped to chapters. The pull sync reads them from Jellyfin 10.10+
(`GET /MediaSegments/{itemId}`, one request per item, no bulk endpoint), bounded
because of that: only for a tenant with a cut rule that has `after_intro`, only
movies and episodes, at most 2000 items never looked up per sync plus a rotating
window of 200 already looked up (so a large library is refreshed over several
syncs), four requests at a time, a 60 second deadline for the phase, and the
phase stops at the first sign of a server without the API (401/403/404/405/501
before any success) or after five failures in a row. Whatever was not looked up
keeps its stored value; a missing API, a failure or an item without segments
never fails the sync. A rule set created after the last sync gets its segments
from the next sync (periodic or manual). The segment calls were exercised
against a fake server; they have not been run against a real Jellyfin.

Every write to a rule set (REST create/update/delete, MCP `create_cut_rule_set`)
triggers the materializer like a break template does. It is a normal
regeneration: a timed channel is regenerated whole, while a continuous channel
keeps the head frozen by the freeze margin (and an item already started or
split inside it), so a changed set takes effect from the first airing after the
head.

REST: `POST/GET /cut-rule-sets`, `GET/PUT/DELETE /cut-rule-sets/{id}` (a PUT
replaces the set) and `POST /cut-rule-sets/preview` (rules plus a sample item:
kind, `duration_ms`, optional chapters and media `segments`; returns `cuts_ms`, `segments_ms`, the
matching `rule_index`, `source` and notes, storing nothing); MCP:
`create_cut_rule_set`, `list_cut_rule_sets`, `preview_cut_plan`; web UI: Cut
rules (list, rule editor and a live preview for sample items).

### Authoring endpoints (item editing, previews)

- `PUT /blocks/{id}/items/{item_id}` with `{position?, trim_duration_ms?}`
  (at least one): `position` is the 0-based target index, the block's
  positions are rewritten contiguous (0..N-1), `trim_duration_ms` must be
  positive; an absent/null field is left unchanged. 400 on a bad value,
  404 on unknown block/item, 409 on a dynamic block. Triggers a (debounced)
  regeneration like the other item writes.
- `PUT /filler-pools/{id}/items/{item_id}` with `{weight}` (positive
  integer): manual pools only, 409 on a dynamic pool.
- `POST /catalog-items/preview-criteria` with a condition tree as the body
  (or `{"collection_id": "..."}`; same validation as a collection's query): returns
  `{match_count, total_duration_ms, sample}` where `sample` is up to 20
  matching catalog items ordered by title. It uses the same matching clause
  (`catalogsync.Query.Where`) as dynamic block/pool resolution and persists
  nothing.
- `GET /channels/{id}/epg-preview?start&end` (epoch millis, both optional,
  same overlap filter as `GET /epg`): a dry-run of `reset-epg`. It runs the
  materializer's own loader and expansion (`Materializer.PreviewReset`) from
  the stored configuration as if the grid were reset now (rotations from the
  start, no freeze margin) and returns the entries in the same JSON shape as
  `GET /epg`. Nothing is written: `epg_entry` and `epg_revision` are
  untouched. Entry ids are throwaway, and random/weighted filler picks may
  differ from a later real reset. It takes `group_tail=1` like `GET /epg` (the
  admin guide uses it); the clips of a run cut by `start`/`end` are the ones
  inside the window, since a preview has no stored rows to complete it from.
- `GET /channels/{id}/epg-warnings`: problems in the channel's stored
  configuration that leave parts of its grid silently empty, as
  `[{kind, block_id, block_item_id?, message}]` (empty list when none). Today:
  `criteria-error` (a criteria item or dynamic block whose query fails to
  evaluate), `series-error` (an unreadable series spec) and `filler-ratio` (the
  fillers configured between two programs of a continuous item or dynamic block
  can exceed half of its shortest program; informational). It runs the
  materializer's own loader, which also logs each such item as an error with
  its ids; the item airs nothing until fixed. The admin guide shows them next
  to the other block warnings, and `preview_channel_grid_reset` returns the same
  list as `warnings`.
- MCP equivalents: `update_block_item`, `update_filler_pool_item`,
  `preview_criteria`, `preview_channel_grid_reset`.

### Collections (migration `0014`)

A `collection` is a tenant-scoped, named, reusable catalog query: `id`,
`name`, `description`, the condition tree (`query`) and timestamps, and
nothing else. Dynamic blocks (`block.collection_id`), dynamic filler pools
(`filler_pool.collection_id`) and criteria block items (`collection_id` inside
`block_item.spec`) reference one by id; there are no inline queries any more.
Playback order, amount, interleave settings and seeds stay on the block or
item, so one collection can be played in different orders by different users.
Names are labels, not unique (the migration names a collection after its
first owner, and a block and a pool may share a name); `tenant_id` leads
`idx_collection_tenant_name`, `idx_block_collection` and
`idx_filler_pool_collection`. `block_item` has no `tenant_id`, so the
expression index `idx_block_item_collection` (on
`json_extract(spec, '$.collection_id')`, partial to criteria items) leads with
the collection id; the usage query joins `block` to filter by tenant. The
foreign keys are `NO ACTION`, the safety net behind the API's 409; the API
also checks that a referenced collection belongs to the tenant (a collection
of another tenant is indistinguishable from a missing one: 404/400).

Source of truth: once an owner has a collection, the collection is the only
query it evaluates. The old `criteria` columns (`block`, `filler_pool`) and
the embedded `query` of a criteria item's spec are **deprecated**, kept and
never dropped in this version: they are the pre-migration snapshot, the way
back from a failed migration, and are no longer updated (editing a collection
does not touch them). They are read only while `collection_id` is NULL, i.e.
for a row still held for review. A block or pool counts as dynamic when
`collection_id IS NOT NULL OR criteria IS NOT NULL` (the API exposes
`dynamic`, `collection_id` and `collection_name` and never `criteria`);
reverting one to manual through the API clears both. They go in a later
version.

Migration (`catalogsync.MigrateToCollections`, at startup after
`MigrateLegacyCriteria`, inside the 30-minute upgrade budget): every dynamic
block, filler pool and criteria item without a collection gets one. Owners are
taken in a stable order (tenant, name, id; blocks, then pools, then criteria
items named "<block> - item N") and owners whose canonical trees (`Query.JSON()`
equality, so child order matters) are identical within a tenant share ONE
collection, named after the first. For every owner the matched set of its
stored query ("before") is compared with the collection's ("after", signatures
of the ordered ids, evaluated once per distinct query) and the stored collection
text is read back and checked against the compared tree; any mismatch, or a
stored query that does not parse, applies nothing, keeps the owner on its stored
query and records a `criteria_migration_review` row (counts, differing items;
`owner_type` is now `block`, `filler_pool` or `block_item`), exactly like the
legacy review. Writes go in transactions of 200 owners; it is idempotent and
re-runnable (only owners without a collection are looked at, an existing
collection with the identical tree is reused) and logs progress. Legacy-format
rows (the pre-tree shape) are skipped: they stay held until their proposal is
applied. `POST /criteria-migration-review/apply` (`{owner_type, owner_id,
query?}`, MCP `apply_criteria_review_proposal`) creates or reuses a
collection from the proposal (or the query sent, required when a legacy value
has no translation) and points the owner at it; the legacy snapshot stays.

`ResolveDynamic` groups the dynamic pools and blocks by collection and
evaluates each distinct collection once per pass, however many blocks and pools
use it (`ResolveCollection` does the same for one edited collection). The
materializer reads a dynamic block's query with a join on the collection
(`COALESCE(collection.query, block.criteria)`), and a criteria item resolves its
collection with one primary-key read per item per run; neither adds per-row
cost to query evaluation.

REST: `POST/GET /collections`, `GET/PUT/DELETE /collections/{id}`,
`GET /collections/{id}/usage` (blocks, filler pools and criteria items that
use it), `GET /collections/{id}/preview`. `PUT` replaces name, description and
query; a changed query re-resolves the dynamic blocks and pools that use it and
triggers the materializer, and criteria items follow it live. `DELETE` of a
collection in use is 409 with the usage listed in `{error, usage}`. Breaking
change to the request shapes (the web UI and MCP are the only clients):
`POST/PUT /blocks` and `/filler-pools` take `collection_id` instead of
`criteria` (a non-null `criteria` is a 400 pointing here), criteria block items
take `criteria.collection_id` instead of `criteria.query`, and
`PUT /blocks/{id}/items/{item_id}` takes `collection_id` instead of `query`;
responses carry `collection_id`, `collection_name` and `dynamic` instead of
`criteria`. `POST /catalog-items/preview-criteria` still takes a bare tree (the
collection editor's live preview) and also `{"collection_id": "..."}`. MCP:
`create_collection`, `update_collection`, `delete_collection`,
`list_collections`, `get_collection`, `get_collection_usage`,
`apply_criteria_review_proposal`; `create_block`/`update_block`,
`create_filler_pool`, `add_block_item` and `update_block_item` take
`collection_id`; `preview_criteria` takes a tree or a collection id.

Web UI: a Collections page (list with usage counts, create, delete showing the
409 and the users) and a collection editor (query builder with live preview, the
usage panel, and a confirmation listing the users before a changed query of a
shared collection is saved). The dynamic block editor, the new-block dialogs,
the schedule item's create-block panel, the filler pool editor and the criteria
item add/change panels pick a collection (with "New collection" inline); the
query builder lives only in the collection editor. The review banner's "apply
the proposal" produces a collection.

### API

- `GET /epg`: the full materialized schedule for every channel in the
  tenant, over the materialization window (`PLAYHEADTV_EPG_WINDOW`, 48h by
  default; past entries are kept, so a client passes `start`). This is the
  "XMLTV" of the project, as JSON — one bulk fetch instead of a client polling
  per-channel or per-minute. Query: `channel_id`, `start`/`end` (epoch
  millis, overlap filter) and `group_tail=1`. The response is a JSON array
  ordered by channel then start, and carries everything a client needs to
  show and play an entry with no further request:
  - Every entry keeps the stored fields (`id`, `tenant_id`, `channel_id`,
    `kind`, `catalog_item_id`, `start_at`, `end_at`, `source_schedule_item_id`,
    `generated_at`) and adds, each one omitted when absent: `media_source_id`,
    `external_id` (the source's own item id, what the stream and image URLs
    are built from), `item_kind` (the catalog kind, not the entry `kind`),
    `title`, `series_title`, `season_number`, `episode_number`,
    `episode_number_end`, `series_external_id`, `duration_ms` (the item's own
    length; `end_at - start_at` is what is scheduled), `watermark` (the
    resolved watermark, see "Watermark") and the image tags
    `image_primary_tag`, `image_backdrop_tag`, `image_thumb_tag`,
    `series_image_primary_tag`. The backend never serves images: a client
    builds `<media server>/Items/<external_id or series_external_id>/Images/
    Primary?tag=<tag>` itself. Required for playing (the fields clients have
    always used) versus optional (everything added): a client treats the
    additions as optional and, when `external_id`/`title` are missing (a
    backend that predates them), looks the catalog item up per id.
  - Without `group_tail` there is one row per stored `epg_entry`, tail clips
    included. With `group_tail=1` each run of back-to-back `tail` entries of
    one channel (no other kind, gap or channel in between) is replaced by one
    item: `{id (first clip's entry id), tenant_id, channel_id, kind: "tail",
    title: "Tail", start_at, end_at, clips: [{id, catalog_item_id,
    external_id, title, start_at, end_at, duration_ms}, ...]}`, clips in
    playing order, none omitted: a run cut by the requested window is
    completed on both sides, so the same run has the same id and span through
    any window and a client can play the whole block, and keep playing it
    offline, from this one item. A group has no `catalog_item_id`. Grouping is
    opt-in because it changes the shape of the array (a client that only
    understands flat entries would play one clip for the whole block); the
    stored rows are never grouped. `fallback` entries are not grouped.
  - Cache validators: `ETag` (`"<epg revision>-<hash of the body>"`, so it
    also moves when catalog metadata changes without a new revision),
    `Cache-Control: no-cache` (storable, always revalidated), `Vary:
    Authorization, Accept-Encoding`, `X-Epg-Revision`; `If-None-Match` gets
    `304`. A body over 4 KiB is gzip-compressed for a client that sends
    `Accept-Encoding: gzip` (the representation then has a `-gzip` ETag
    suffix); a tail run of hours of 10-second clips is mostly repetition and
    compresses roughly 6-7x.
- Guide display (client and web UI, not the API): `GET /epg` keeps returning
  every kind, since playback needs them, but the viewers only draw `program`
  and `fallback` entries (a fallback is the channel's own "nothing scheduled"
  filler and stays a cell of its own). A hidden entry's time is attributed to
  a neighbouring program, whose cell is drawn over the widened span:
  a `preroll` extends the next program backwards; a `postroll`, `midroll`
  or `tail` (a grouped run counts as one) extends the previous program
  forwards. With no program on that side the other neighbour takes it; with
  none there either, or when it does not touch the neighbour (a real gap
  sits between them), the entry is left out and its time shows as a gap. A
  fallback never absorbs anything. The displayed span is separate from the
  real one: a cell keeps its entry's own `start_at`/`end_at`, and "on air
  now" (live highlight, progress, select-to-tune) is decided on the displayed
  span, so a live filler counts as the program it was attributed to. The cell
  and the client's details panel show the displayed span. The player OSD
  follows the same rule (`client/lib/osd_subject.dart`, built on
  `buildGuideCells`): while a hidden entry is on air it describes the program
  the guide attributes that entry to, never the filler. Its title, "Ends" time,
  start-end times and progress all use the program's displayed span (progress
  runs across program plus filler, read at where the on-air picture is in the
  schedule: the unit's start plus its controller position net of the file
  offset, so a program that begins reads 0:00 even when the wall clock is a
  little ahead of the picture; the wall clock is the fallback while the
  position is unknown, e.g. on the gap slate), "Up next" is the next
  visible program, and it only re-announces when the attributed program
  changes, so a filler of the same program (or another clip of a tail) never
  pops it again. A `fallback` is described as itself, and so is a hidden entry
  nothing could be attributed to. When the filler already on air at tune-in
  belongs to a program that ended before the first fetched entry, the client
  reads the entries before it from the cache to find that program. A `tail`
  can last hours, so it is attributed to the previous program only for its
  first 30 minutes (`tailAttributionCap` / `TAIL_ATTRIBUTION_CAP_MS`, one
  constant per platform), measured from the start of the run of tail entries
  (a postroll before it does not eat the budget). Past the cap the rest of the
  tail belongs to no program, not to the next one either: the guide shows it
  as a gap (no live cell, nothing to select-to-tune), and the OSD shows the
  channel's number and name as its title, with no program times, no "Ends"
  and no progress ("Up next" stays). A tail with no previous program (nothing
  before it, or a fallback in the way) can still fall to the next program, but
  only when the whole tail fits in the cap. The cap applies to tails only;
  preroll, midroll and postroll are short and keep their rule. The OSD's
  subject is re-read on its own tick, so it flips to the channel when the cap
  passes without any entry change. The guide and the OSD feed
  `buildGuideCells` the same context: the cache entries from
  `guideContextLookback` (2h) before their first instant, which covers the
  cap plus the filler between a program's end and its tail with a wide margin
  (the cache query returns anything overlapping the span, so the program's own
  length does not matter) and stays inside `cachePastRetention` (6h); the
  guide drops the cells that end before its window. The web UI's "Show filler"
  toggle (UI state only) draws every entry at its real span instead. The rules are one pure function per platform
  (`client/lib/epg_guide.dart`, `webui/src/lib/guideDisplay.ts`) with the
  same unit tests. There is no per-channel or per-block setting.
- `GET /epg/meta`: `{revision, valid_until, freeze_margin_ms, window_ms}` only —
  cheap enough to poll often. `window_ms` is the materialization window
  (`PLAYHEADTV_EPG_WINDOW`): the most programming a client can hold, which it
  caps with its own limit (see "Client offline cache"). Clients keep the full
  `/epg` response locally and refresh it conditionally (`If-None-Match`), see
  below.

### Edge caching

This is a deployment concern, not part of the API contract. For cloud-sync,
the Go server sits behind Cloudflare's ordinary reverse-proxy CDN
(orange-cloud DNS) and serves `GET /epg` with standard `Cache-Control`
headers; the cached response is purged when `epg_revision` changes, so D1 is
only read on an actual schedule change, never on a routine poll. Self-hosted
deployments may or may not sit behind any CDN — `GET /epg`/`GET /epg/meta`
work the same either way.

### Why no realtime sync is needed

Because the EPG is deterministic (wall-clock time + a materialized
timeline), any device computes "what's playing now" the same way any TV
tuner would: look up the `epg_entry` covering the current instant. Two
devices watching the same channel land on the same position without any
session/state server in the loop — this is what makes the "no WebSocket"
constraint free rather than a limitation.

## Client offline cache

Principle: offline first, excluding the video flow itself. Playback already
talks to the media server directly (on the user's own network, whether the
backend is self-hosted or hosted), so with a complete local copy of the
schedule a client needs no backend to keep playing, for about as long as the
copy lasts. Everything below is the client (`client/lib/cache/`); the backend
only gained `window_ms` and the configurable window.

- **Storage.** One SQLite file (`schedule_<hash of the backend address>.db`
  under the app support directory; one file per backend address, so pointing
  the app at another backend never shows or wipes the first one's schedule),
  the same engine on Linux/Windows now and Android/Android TV later. The
  package is [`sqlite3`](https://pub.dev/packages/sqlite3) (3.x), which
  bundles SQLite through Dart build hooks (native assets) for every Flutter
  target, so there is no per-platform plugin, system library or ABI to
  manage; `path_provider` finds the directories. Tables: `meta` (validator,
  revision, last sync time, backend window), `channel` (the channel list, in
  `number` order) and `epg_entry` (one row per entry, its JSON being the
  backend's own `GET /epg?group_tail=1` shape: titles, source ids, art tags,
  and for a grouped tail EVERY individual clip). `PRAGMA user_version` is the
  cache schema version (`scheduleCacheSchemaVersion`): opening a file of
  another version recreates it empty and the next sync refills it. That bump
  and the "Clear cache" action in Settings are the only things that ever empty
  the cache: a network or auth error never does.
- **Images.** The posters of the cached window and the channel logos are files
  in an `art` folder under the app cache directory, one per URL (named by a
  hash of it, since a URL carries the media server key), with a size cap
  (96 MiB) and least-recently-used eviction (a file's modification time is its
  last use). The art comes from the media server (or wherever a logo points),
  never from the backend, so it is shown from disk with the backend down.
  `ArtImage` reads the file when it is there and the network otherwise.
- **Refresh.** The only code that asks the backend for schedule data is the
  background `ScheduleSyncer`. It runs at startup, every 15 minutes
  (`--dart-define=EPG_SYNC_INTERVAL_MINUTES`) and, after a failure, again every
  30 seconds until one succeeds (this is how a returning connection is noticed
  on every platform, with no connectivity plugin). One refresh is `GET
  /epg/meta` (the window), `GET /channels` and one `GET /epg?group_tail=1`
  for every channel from the top of the previous hour, sent with the
  `If-None-Match` of the copy held: an unchanged guide is a `304` of a few
  hundred bytes, a changed one downloads the new body, which is decoded off
  the UI isolate and replaces the rows it differs on. Offline tolerance is the
  window minus the time since the last successful refresh, so the buffer is
  topped up often rather than only when the revision changes. Against a
  backend that predates the enriched `GET /epg`, the syncer resolves the
  missing source ids and titles once, before storing.
- **Reads.** The player's working set (the program on air plus the next N),
  the EPG grid, the mosaic and channel switching read only the cache; the
  rolling per-slice fetching is now a view over it. The channel list and the
  stream-selection rules (`StreamPreferencesService`, which keeps its own
  copy in `shared_preferences` and degrades to "no preference" when absent;
  an unreachable backend is never cached as an answer) are the other things
  read at runtime.
- **Window.** The effective window is the smaller of the backend's
  (`window_ms`, 48h assumed when absent) and the client's own limit (a
  setting, 48h by default, Settings -> Offline cache). Reads never go past it
  and a sync stores nothing past it; the validator is not offered for a
  different limit, so raising it downloads again.
- **Cache exhausted** (offline for longer than the window; the backend is
  asked once more to tell this from "the backend really has nothing"): the
  client's own setting, in `shared_preferences`, decides. *Loop the last known
  programming* (default) replays the last 24h of what the cache holds as
  time-shifted copies, in order, over and over; *hold the slate* crossfades
  into the local slate and waits. There is no backend setting for this.
- **Reconnect.** A refresh that brings a different schedule applies it exactly
  like an admin grid reset on the channel on air (`reset-epg` regenerates the
  on-air entry): if what is on air differs from what the new schedule has on
  air now (item and span), the player reloads the channel and switches to it;
  otherwise only the programs after the one on air are replaced in place,
  without restarting it, and only when no preload is parked for the old next
  one (the next change or refresh retries). A channel playing the exhausted
  fallback leaves it as soon as the cache has real programming again.
- **Auth.** A hosted sync uses the device token. A rejected or expired token
  (`401`/`403`) is a failed refresh like any other: the cache is kept, playback
  continues from it, and the next attempt (30 seconds later) sends the
  configured credentials again, so the cache is refreshed as soon as the
  backend accepts them. The device token is static configuration today, so
  there is no token renewal flow.
- **Six buttons.** Settings -> Offline cache (Up/Down move the focus, Select
  changes a value or runs an action, Back leaves) holds the exhausted-cache
  setting, the window limit, "Sync now" with the age of the copy and how long
  playback could go on without the backend, and "Clear cache" behind a
  confirmation.

## Client dimmer

A software dimmer lowers the picture's brightness: `DimmerOverlay`
(`client/lib/widgets/dimmer_overlay.dart`) is a black layer of adjustable
opacity, the last child of the player page's `Stack`, so it covers the video,
the OSD, the EPG and the pickers alike. It ignores pointers and never takes
focus. It is driven by `Dimmer.active`/`Dimmer.level` (`lib/dimmer.dart`)
through a `ListenableBuilder`, never `setState` on the page (GL-texture rules).
The mosaic has no layer.

- **Level.** 10 to 80% in steps of 10, 50 by default (`dim_level_percent` in the
  config file, else the user's saved level from Settings -> Dim level or the
  dialog).
- **Persistence.** Both the level and the on/off toggle are saved
  (`dim_level_percent`, `dim_active`), so an update, a crash or a power cycle
  brings the picture back as the viewer left it and nobody has to find the
  button again after every install. An installation nobody dimmed has no
  `dim_active` and starts undimmed, which is what a first run should look
  like; a viewer who dims and then dislikes the dark start turns it off once
  and that is remembered too.
- **OSD.** A dimmer button sits next to the audio and subtitle buttons. In OSD
  focus mode Select toggles the dim. While dimmed, a level chip appears beside
  the button: Select on it opens the level dialog, the six-button path from the
  OSD. Holding Select on the button (or a long press with a pointer) opens the
  same dialog: an accelerator only.
- **Dialog.** Left/Down lower, Right/Up raise, Select saves, Back cancels
  (restoring the previous state). The dim shows live behind it; saving leaves
  the dimmer on. Settings -> Dim level opens the same dialog.

## Client always-on clock

The OSD already shows the local time in its top-right corner, but only while it
is on screen. The always-on clock keeps it there with the OSD closed: a setting
(`Settings -> Always show the clock`), on or off, off until chosen.

- **Where.** `ClockOverlay` (`client/lib/widgets/clock_overlay.dart`) is a
  `Positioned` child of the player's `Stack`, last but one: above the video, the
  watermark, the OSD, the guide and the dimmer (a dimmed picture is exactly when
  you want the time). It is drawn on the player only, never on the mosaic, and
  it stays hidden whenever it would duplicate one of those.
- **When.** It steps aside while the OSD is visible (the OSD draws its own clock
  in the same corner) and while the full-screen guide is open (which has its own
  time header). Both are `ValueListenables` the page already owns
  (`_osdVisibleNotifier`, `_epgOpenNotifier`), so nothing here calls `setState`
  on the player page (GL-texture rules).
- **Look.** Opacity from 20 to 100% in steps of one choice (default 80) and a
  size factor over the OSD clock's font size, 0.6 to 2.0 (default 1.0). Both are
  percent-styled in Settings and persisted (`clock_opacity_percent`,
  `clock_scale`); the state is `ClockOverlaySettings` (`lib/clock.dart`), seeded
  from `SettingsService` at boot the way the dimmer's level is. The text carries
  a black shadow so it stays readable over bright picture at low opacity; no
  background box is drawn.
- **Ticks.** A one-second timer that only calls `setState` when the minute it
  shows actually changed, so the widget repaints twice an hour at most.
- **Not configurable.** The corner is fixed (top-right, where the OSD puts its
  own clock) and the color is the player's white: the feature exists to keep the
  time visible, not to become a widget editor.

## Client boot: what runs before the first frame

The boot is measured, not estimated: `BootTrace` marks each phase and logs
`boot: phases (ms since start)`. Numbers from a desktop run (release build,
warm cache, backend on the LAN):

| phase | measured |
|---|---|
| launcher: release check at GitHub (`prepare`) | 650-900 ms, budget 120 s |
| process start to `main()` (Flutter engine, GL) | ~210 ms |
| `main()` to first frame | 6-9 ms |
| offline runtime open (`runtime_open`) | 44-892 ms (warm/cold page cache) |
| channel load (`runtime_open` to `channel_on_air`) | 136-932 ms |
| backend on the LAN (`/healthz`) | 0.8 ms |

Two things follow from that table, and both are implemented:

- **No network before the picture.** The release check is the only network call
  the start used to wait for; it now runs after the app is starting (see
  "Headless auto-update"). A prefetched DNS/TCP connection would buy nothing
  here (0.8 ms on a LAN), so there is none.
- **The offline runtime opens early.** `main()` starts
  `OfflineRuntime.ensure()` (only for a configured install — a pairing run has
  no business opening a schedule cache) before the engine is registered, so its
  SQLite open overlaps the engine start and the first frame. Every later caller
  gets the same instance: `ensure()` hands back the future already in flight,
  so this costs nothing and cannot open a second cache.

### Boot prefetch (the channel's own resolve, before the UI)

The expensive half of starting a channel is not in the app: on a plugin-backed
channel it is one backend call, `GET /catalog-items/{id}/play`, which waits for
an extractor run (250-350 ms when the plugin's cache is warm, seconds when it is
not). Everything that call needs is known before the first frame — prefs say
which channel, the guide is on disk — so `prefetchFirstChannel()`
(`client/lib/main.dart`) resolves it in parallel with `runApp` and the page's
own boot, and stores the answer in `StreamCache`, which is the same cache the
player's `_entrySource` reads. The work is therefore reused, not merely warmed.

The rules it follows, each one there to avoid making something else worse:

- Only for the channel the app goes **straight into** (`lastWatchedChannelId`).
  A mosaic start has no route yet, and pre-resolving a guess would cost the host
  an extractor run for a channel nobody opens.
- Only the **head unit**, computed by `headUnitOf` — the same choice
  `_loadAndPlayChannel` makes (the program on air expanded by
  `unitsOfProgram`, joined where it already started), so the prefetched url is
  for the item and position playback will actually ask for.
- Only when that unit needs a plugin at all: a media-server item is a plain url.
- Only through the **normal mode order** (`pair` first), so a prefetch never
  opens a remux process on the host for nothing.
- Never awaited and never retried: anything that fails (no cache, offline,
  extractor down) just loads the old way one settle later.

## Plugin playback cache

A plugin-backed item has no url until somebody asks the backend, and that answer
is expensive on the host (an extractor run, plus a remux process for `remux`),
so it is kept in memory for `pluginStreamReuse` (45 s) by
[`StreamCache`](client/lib/cache/stream_cache.dart). It is deliberately not part
of the offline cache: nothing survives the process and nothing is written to
disk, because the url carries the plugin's token, expires, and a `remux` stream
exists only while its process does.

The cache is keyed by the item and by the **position bucket** the request falls
in (`streamPositionBucket`, 2 s), which is what lets a resolve started at boot
(when `now` is not yet the position playback will use) be the one playback
uses. A shared answer never changes what the picture should show:

- a `pair` (or a `single` file) is the item from its own zero and the player
  seeks to the position it wants, so sharing costs nothing;
- a `remux` is cut where it was asked to start, so a reused one plays from the
  position it was cut at — at most one bucket behind the wall clock, the same
  slop `epgSyncTolerance` already allows between the guide and the picture, and
  the player reads every position from that stream's own start
  (`_playbackOffset`), so the OSD stays consistent with what is on screen.

A url is dropped the moment an open with it fails (`forget`), whatever the reuse
window says.

## Leveling the channels

A channel change used to be a level jump: over 30 s windows, a sample of the
library spans 9.5 dB between two channels and up to 10 dB inside one film. The
client levels it with a static gain per piece of audio (ReplayGain, not a
compressor): nothing is re-encoded and the audio path is untouched.

- **Where the number comes from.** The device measures it (`LoudnessChannel.kt`
  over `Media3Probe.kt`): ExoPlayer reads the stream, the platform MediaCodec
  decodes it, and a `ForwardingAudioSink` reads a duplicate of every decoded
  buffer, so nothing is heard and the audio continues untouched. The measurement
  is ITU-R BS.1770: K-weighting, 400 ms blocks with 75 % overlap, a -70 LUFS
  absolute gate and a -10 LU relative gate.
- **Why on the device.** Not a shortcut — the accurate path. The platform
  decoder applies Dolby downmix/dialnorm, so an ffmpeg measurement behind the
  backend lands up to 8 dB away from what a viewer hears (AC3 -7.8 dB, EAC3
  -3.9 dB, measured on the same windows).
- **A position can be asked for.** ExoPlayer seeks by reading the container
  index with range requests, which is how a tune-in mid-programme already works.
  `MediaExtractor` cannot seek over HTTP on the same box (its sample time stays
  at 0), so a probe built on it can only read a file's head.
- **The gain**: `min(10^((target - LUFS - preamp)/20), 10^((-1 - peak - preamp)/20))`,
  where `preamp` is the global gain above unity described below. Two terms, both
  attenuations, the smaller wins: one brings the average level to the target, the
  other keeps the peak under -1 dBFS.
- **The pre-amp** (`AudioPreamp.kt`): a `LoudnessEnhancer` on the output mix
  (audio session 0), which is the only gain above unity the app can get — a
  player volume stops at 1. Measured before it was written: it applies the
  requested gain exactly (+6 -> +6.00 dB, +40 -> +39.90 dB) and has a limiter, so
  peaks pushed past full scale compress instead of clipping. Attaching it to the
  mix instead of a player session is what keeps `video_player_android`
  unpatched. Where the platform refuses it, `hasControl()` is false and leveling
  degrades to attenuation only.
- **Defaults**: `loudness_target_lufs` -18, `loudness_preamp_db` 12. Over a
  measured sample of 47 items (loudest of two windows each) that lifts the
  library 4.1 dB on average, 36 of 47 items louder, and takes the final spread
  from 28 dB to 1.3 dB.
- **How it is applied**:
  - during the preload the item is measured at its start and remembered, so the
    800 ms crossfade into it carries the right gain instead of a correction
    heard seconds later;
  - on a channel change the new channel starts from its own prior (the level it
    was last leveled to), never from the level of the item that was on air: that
    inheritance played a loud item at a quiet item's volume until the correction
    landed, which is heard as the channel arriving several dB too loud. With no
    prior either (a channel never watched), it enters at the level of the loudest
    material the library holds and the correction goes up, so the error is always
    on the quiet side. The point being watched is then measured (5 s of audio,
    about 3 s of wall clock) and the correction ramps in — over 2 s when it goes
    up, over 4 s when it comes down;
  - an item measured before enters at its own gain and nothing is decoded;
  - a window with too little signal (under 4 blocks) keeps the current level;
  - a mid-programme window levels that playback only; it is not stored as the
    item's level, so an unlucky quiet scene cannot become the item's gain;
  - before an item's gain is remembered, a second window is measured at 40 % of
    the item. Two windows of the same item differ by 2.6 dB typically and 9.8 dB
    at p90; past 6 dB they are not describing the same audio (a malformed file
    measured 14 dB apart at two offsets), and the item is then left unleveled
    rather than remembered with a guess;
  - a different audio track is a different master — one film carries a 2.0 dub at
    -6.2 LUFS and a 5.1 track at -26.9 — so choosing another track forgets the
    item's gain and levels again at the current position.
- **Config**: `loudness_enabled` (default true), `loudness_target_lufs` (-18) and
  `loudness_preamp_db` (12). All three are also settable in Settings, since
  Android has no config file to edit.

Cost on the box (Amlogic S905X over 5 GHz): 5 s of audio takes 2.6-4.6 s of wall
clock, 10 s takes 5-10 s, and a probe running while a channel plays drops no
frame. The preload measures two windows, which fits in the 20 s preload lead.

## Client preload lead

The client prepares the next unit (creates and initializes its controller) a
"lead" before the current one ends. The configured lead is 20 s by default,
5 to 60 s (`preload_lead_seconds` in the config file, or Settings -> Preload
lead, which steps through fixed choices with Select). `lib/preload_lead.dart`
holds the pure policy:

- **Adaptive.** `PreloadLeadTracker` keeps the duration of the last four preload
  attempts (create + initialize, failures included). The effective lead is
  `max(configured, 2 x slowest + 2 s)`, capped at 90 s (or the configured value
  when larger), so a slow library gets more time without tuning and the lead
  decays back as fast preloads replace the slow samples.
- **Short items.** An item shorter than the lead preloads right after it starts;
  the `minTransitionInterval` floor still applies to its length.
- **No dead stop.** A failed attempt is retried after 1, 2, 4, 8 s, then every
  16 s, and the status shows an error only after four failures, without ever
  giving up. When the transition arrives before the preload is ready, the
  current item (or the gap slate) stays on screen and the crossfade happens as
  soon as the preload is ready. One preload runs at a time per channel; a
  channel switch drops any attempt in flight and disposes its controller.
- **Promotion.** The parked controller has not read anything since it was
  opened, so the connection it holds can be closed as idle; the engine reports
  that only when the item is asked to play, which is the transition itself. So
  the incoming item is started (and judged, by `judgePromotion` in
  `lib/playback_promotion.dart`) before the fade begins: an item that shows
  progress is promoted at once, a failure reopens the same unit
  (`playbackPromotionAttempts` = 2) and, still failing, the channel resumes it
  with a hard cut. The fade never starts on an item that has not shown a
  picture, the item on screen keeps the screen meanwhile, and no unit is ever
  skipped. The same promotion runs before the slate leaves after a gap hold,
  where the item waited even longer.
- **End of a crossfade.** The transition follows the schedule entry, not the
  file, so the outgoing file may still have material left. Its volume is set to
  zero, it is paused and disposed (through the GL queue) when the ramp ends,
  whatever its platform calls do (each is bounded and never throws), so at most
  one controller is audible. The next transition is timed from the moment the
  incoming item started (minus the point it joined at after a resync), so
  transitions do not drift by the length of the ramp.

## Client video engine

The client talks to the video engine only through `lib/video/video_engine.dart`:

- `VideoEngine` creates playbacks (`createNetwork`, `createAsset`), does the
  one-time process setup (`register()`, called from `main()`) and names itself
  and its decoder policy for the debug overlay. `videoEngine` is the one in use;
  `useVideoEngine` replaces it (a test double, or a per-platform choice at
  startup) before any playback exists.
- `VideoPlayback` is one media being played or prepared: `open`, `play`,
  `pause`, `seekTo`, `setVolume`, `setLooping`, a `state` snapshot
  (`PlaybackState`: initialized, playing, buffering, position, duration,
  buffered-until, frame size, volume), the `tracks` it found (`MediaTracks`),
  `selectAudio`/`selectSubtitles`, `tune` and `dispose`. `VideoSurface` draws one.
- Tracks are app-owned types (`AudioTrackInfo`, `SubtitleTrackInfo`,
  `VideoTrackInfo`), not the engine's. `selectAudio`/`selectSubtitles` take the
  0-based position in the matching list of `MediaTracks`, never the container's
  absolute stream number; the stream-selection resolver and the track picker use
  the same positions.
- **Where the stream begins is the mode's business, not the engine's.** A
  `remux` is produced at the position it was asked for, so the player opens it
  where it is and the OSD adds that offset back (`_playbackOffset`); a `pair` or
  a `single` file is the item from its own zero, so the player seeks and adds
  nothing. `_entrySource` returns the difference as `startOffset`, and
  `_createController` carries it back out of the open instead of assuming it from
  `_needsPlugin`.
- Engine-specific tuning is the engine's business. `tune(PlaybackTuning)` carries
  what the app wants (the read-ahead window); the engine adds whatever it needs
  on its own (`FvpVideoEngine` sets `avsync.audio=0`, see AGENTS.md).
- **The native lifecycle serialization belongs to the engine.** `FvpVideoEngine`
  routes `open` and `dispose` through the shared FIFO (`serializeGlOp`, with its
  stall valve) because libmdk's GL threads are process-global; `lib/main.dart`
  and the mosaic never call `serializeGlOp`. A new engine decides for itself
  whether it needs a queue.
- **A playback can carry a second, audio-only source** (`createNetwork(uri,
  audioUri:)`), which is how a plugin's `pair` is played (`docs/PLUGINS.md`
  "Modes"). `FvpVideoEngine` attaches it with mdk's `setExternalAudio` **after**
  `initialize`, then activates the track: preparing a media url resets the
  external media (mdk's own note on `setMedia`), so attaching it first is silent.
  The caller's `seekTo` runs after the attach and mdk applies it to the whole
  timeline — measured on a real pair, a tune-in 5874 s into an item had its audio
  correlate 0.96 with that position and 0.09 with the file's start, and the
  picture opens in ~300 ms. mdk does not list the external track in
  `getMediaInfo()`, so `tracks` (the overlay, the audio picker) show no audio for
  a pair, which is right for one track. `VideoEngine.supportsPairedAudio` decides
  whether the player offers `pair` at all (`pluginPlaybackModes`). `ExoVideoEngine`
  reports true as well, through a player of its own — see "Paired playback on
  Android" below.
- Two implementations exist, both in `lib/video/`: `FvpVideoEngine` (libmdk,
  every platform, the default) and `ExoVideoEngine` (ExoPlayer/Media3 through
  `video_player_android`, Android only; see "ExoPlayer engine" below). They share
  `ControllerPlayback`, the playback control, state and view on top of the
  `video_player` plugin's controller. Only `lib/video/` imports `fvp` or
  `video_player`; `test/video_engine_boundary_test.dart` fails when anything else
  does (its one exception is the ExoPlayer engine test, which fakes the plugin). The crossfade A/B model, the gap slate and the mosaic
  preview are unchanged and sit on top of this seam.

### ExoPlayer engine

`ExoVideoEngine` exists because libmdk's Android path can leave a TV box with
sound and a black picture, while ExoPlayer drives the platform MediaCodec the
way those boxes' decoders are built for. It is opt-in and never chosen for the
user:

- **Choosing it.** `PLAYHEADTV_VIDEO_ENGINE` (environment), else `video_engine`
  in the config file, else the choice saved in Settings -> "Video player"
  (Android only, applies at the next start), else `fvp`. `exo` anywhere but
  Android, and any unknown value, fall back to `fvp` with a warning in the log;
  the app never fails to start for a bad engine name
  (`resolveVideoEngineKind`, `selectVideoEngine`).
- **Languages.** Stream rules are authored against the three-letter tag a
  container carries (`por`, `eng`), which libmdk reports. Media3 reports the
  shortest form (`pt`, `en-US`). `canonicalLanguage` (`language_code.dart`) maps
  it back to ISO 639-2/B, so the same file resolves to the same track whichever
  engine plays it. Add a pair there when a language is missing; an unknown tag
  passes through unchanged.
- **Tracks.** The plugin reads tracks asynchronously and `tracks` is
  synchronous, so `open()` reads them once. `selectAudio` takes the position in
  that list, translated to the plugin's `groupIndex_trackIndex` id.
- **Limits of the plugin** (not of the app). Embedded subtitles are not exposed:
  `tracks.subtitle` is empty, so the OSD's CC button is dimmed and a rule that
  demands a subtitle language does not match. The read-ahead window cannot be
  tuned. One audio track plays at a time. There is no software fallback: a codec
  the device cannot decode does not play.
- **Audio focus.** Players are created with `mixWithOthers`, so none asks for
  audio focus; otherwise the crossfade's incoming player would make ExoPlayer
  pause the one fading out.
- **No GL queue.** The serialization around create/dispose is libmdk's problem
  (process-global GL threads) and stays in `FvpVideoEngine`; ExoPlayer instances
  are independent.

### Paired playback on Android

A `pair` (a video url and an audio url, `docs/PLUGINS.md` "Modes") does not go
through `video_player_android`: that plugin opens one media source per player, and
a second player would be a second picture. ExoPlayer itself merges — so the app
builds that player on the Android side and takes only the texture from Flutter.

- **The seam is the same.** `ExoPairedPlayback` (`lib/video/exo_paired_playback.dart`)
  implements `VideoPlayback`; `ExoVideoEngine.createNetwork(uri, audioUri: ...)`
  returns it, and `supportsPairedAudio` is true. Nothing above `lib/video/` knows
  the pair is handled differently.
- **One timeline.** The host merges the two sources with
  `MergingMediaSource` (`client/android/app/src/main/kotlin/.../PairedVideos.kt`,
  channel `playheadtv/paired`), so a single `seekTo` moves both — which is what a
  tune-in in the middle of an item needs: the audio of that point, not of the
  file's start.
- **Open means open.** The host answers `create` only once the merged timeline is
  ready, and the answer carries the track lists, so a caller reading `tracks`
  right after an `open` that returned does not depend on a later message. State
  then arrives as samples every 200 ms and at every change; the position still
  passes through `PlayheadClock` (see "A playing position that keeps time").
- **Same limits as the one-source path**, and one more: `media3`'s load control
  is not reachable, so `tune` is a no-op, and subtitles are not exposed (an audio
  track list is: `selectAudio` addresses `currentTracks` the way the app already
  addresses tracks).
- **Audio focus** is not requested (`handleAudioFocus = false`), for the same
  reason the one-source path passes `mixWithOthers`: the crossfade fades two
  players itself.
- **Proved on a device, not in a unit test.** `integration_test/exo_pair_test.dart`
  plays a pair whose two files carry one track each (the video file has no audio,
  the audio file no picture), so a playback that reports both kinds of track can
  only have opened both; it also seeks and requires the position to advance from
  there. On the lab: `client/tool/run_android_suites.sh exo_pair_test`.

The playback surface stays a single, never-rebuilt widget per playback (see the
GL-texture gotcha in AGENTS.md): `buildView()` is called once per
`VideoSurface` build and the player places it through the same
`ValueListenableBuilder` structure as before.

## Client controller lifecycle (leaving the player)

Every `VideoPlayback` (the engine-neutral handle, see "Client video engine") the client creates (the player's crossfade pair,
preloads, the gap slate, the mosaic preview) is registered by
`lib/util/controller_lifecycle.dart` until its dispose has finished, and every
dispose goes through `disposeController` (the engine queues its own native
teardown).

- **Leaving invalidates everything.** `_ChannelPlayerPageState.dispose()` bumps
  `_loadGeneration` (the same counter a channel switch bumps), so every load,
  preload, crossfade and gap hold still in flight sees it has been superseded
  after its next await.
- **A late controller is discarded.** A source can take many seconds to open (a
  cold library file). A controller whose open, seek or `play()` finishes after
  the page is gone, or after the channel changed, is never attached: it is
  silenced, paused and disposed (`discardController`), so nothing stays audible
  over another screen and no connection or GL queue slot is held. The same
  holds for the mosaic preview when the mosaic is left while it opens. A
  controller whose preparation fails is disposed where it was created.
- **Re-entering is a fresh start.** The next load queues its own `initialize()`
  behind the abandoned one in the GL queue, bounded by `glOpQueueTimeout`.
  `leave_during_load_test.dart` covers leaving during a slow open and playing the
  channel again.

## Client logging

The client has one logger, `Log` (`lib/log/`, pure Dart: the updater CLI uses
the same code). Call sites use `Log.error/warn/info/debug/trace(tag, message)`
with a component tag (`player`, `preload`, `crossfade`, `gl`, `controller`,
`sync`, `cache`, `mosaic`, `update`, ...); nothing in app code calls `print` or
`debugPrint`.

**Levels.** `error` is a failure (no media titles or paths), `warn` something
unexpected that was handled (a retry, a fallback; no titles or paths either),
`info` lifecycle summaries (a channel loaded and on air with the time it took, a
crossfade done, a sync outcome, an update outcome), `debug` step-by-step detail
(preload attempts, controller create/dispose, GL queue waits, scheduling) and
`trace` very chatty detail. A message is written when its level is at or above
the active one. A message may be a `String Function()` that is only built when
the level is on.

**Defaults and precedence.** A packaged build (it carries a release version,
`PLAYHEADTV_CLIENT_VERSION`) logs `error` only; a local build (`0.0.0-dev`, what
`flutter run` gives) logs `debug`. Sources, first valid one wins:

1. the `PLAYHEADTV_LOG_LEVEL` environment variable,
2. the `--dart-define=PLAYHEADTV_LOG_LEVEL=` of the build,
3. the `log_level` key of `config.json`,
4. the build default above.

The packaging scripts pass no log define, so a release always starts at `error`
unless the user's environment or config says otherwise. On top of that, Settings
has a "Log level" choice (Error, Warn, Info, Debug) that applies at once and only
until the app restarts: it is never saved, so a forgotten verbose client goes back
to the configured level on its own (`Log.setRuntimeLevel`; `Log.configuredLevel`
is what it returns to).

**On-screen peek.** A build with
`--dart-define=PLAYHEADTV_LOG_PEEK=true` records every level, mirrors the log to
the platform console and draws its last lines in a band at the bottom of the
player (`_LogPeek` in `lib/main.dart`). That build exists for a device whose log
file cannot be read, so the band would otherwise be stuck on screen; the OSD
carries a "Logs on/off" button (`osd-btn-debug-log`, drawn only by such a build)
that hides and shows it at runtime, and the choice is remembered
(`debug_log_visible`). Hiding the band stops the drawing and its one-second
tail read, never the log itself — the file and the console keep receiving
lines. A build without the define never shows the band and never draws the
button, so a release behaves exactly as before.

**File.** `playheadtv.log` in the install's state directory, next to `update.log`
and `state.json`: the install root of the tar.gz install (`PLAYHEADTV_ROOT`),
`${XDG_STATE_HOME:-~/.local/state}/playheadtv` for the AppImage and any other
run, `%LOCALAPPDATA%\playheadtv` on Windows (`logDirectory` in
`lib/log/log_location.dart`). With no usable directory the log stays in memory
(the debug overlay still shows it). Lines are queued in memory and written by a
short timer, never on the calling path, so logging cannot block the UI or raster
threads; the queue is bounded and a failing disk switches file logging off
instead of failing on every line. The file rotates at about 2 MB: the current file plus
`playheadtv.log.1` and `playheadtv.log.2` (6 MB at most). The updater CLI appends
to the same file (tag `update`).

**Redaction.** The logger redacts, not the call sites: the Jellyfin API key and
device token the app holds (also URL-encoded), URL parameters named like `ApiKey`,
`api_key`, `token`, `access_token`, `X-Emby-Token` and `password`, and
`Authorization` header values are replaced by `<redacted>` before a line is stored
or shown. This is a safety net; a message should still not carry a secret. Long
messages are cut at 4000 characters.

**Privacy.** `error` and `warn` lines carry no media titles or file paths. `info`
and below may carry opaque item ids, channel numbers and timings, and exception
text from the platform, which can include a server address. The log file is never
sent anywhere: a user shares it voluntarily, after reading it.

**Sharing.** Settings -> Diagnostics shows the log path and the active level and
has "Create log bundle", which concatenates the rotated files (oldest first) into
`playheadtv-log-bundle.txt` in the same directory and confirms with a toast; it
is operable with the six buttons. The debug overlay (`D`) shows the active level and
the last ten lines (from the logger's in-memory tail, on the overlay's own refresh,
never a rebuild of the player page). The bug report issue template asks for that
bundle. In Diagnostics the Log level row is focusable (the page opens on it; "Log
file" is informational) and Select cycles the level exactly like Settings -> Log
level: until the app restarts, never saved. The debug overlay stays read-only.

**Focus conventions for pages.** A widget that only catches Back from a
focused row (a page-level `Focus(onKeyEvent: ...)` or `KeyboardListener`) must be
non-focusable (`canRequestFocus: false, skipTraversal: true`). A focusable one
covers the whole page, so Down on the last row lands on it (no row highlighted,
Up/Down/Select dead, only Back works). Key events still bubble through it from the
focused row. The page then needs an `autofocus` row of its own. Down on the last
row and Up on the first keep focus on that row (Up on the first may reach the
AppBar controls).

**Backend.** The Go backend uses the same level names through
`PLAYHEADTV_LOG_LEVEL` (`internal/logging`). Unset, nothing changes: messages
go through the standard logger exactly as before. Set, they are filtered by level
and written by a structured text handler on stderr.

## Client window (full screen)

The client opens in full screen by default on desktop, so a TV or console-style
setup needs no one to maximize it and a remote has no way to.

- **Seam.** `AppWindow` (`client/lib/services/app_window.dart`) is the platform
  side, `FullScreenController` the policy over it: edge-triggered (a repeated
  request for the state already applied does nothing) and never throwing, so a
  window the platform will not resize cannot stop the app. Pages never touch the
  window; tests replace the interface (`appWindowOverride`) with a fake.
- **Implementation.** One method channel, `playheadtv/window`, with one method,
  `setFullScreen(bool)`. The Linux runner (`linux/runner/my_application.cc`) serves
  it with `gtk_window_fullscreen`/`gtk_window_unfullscreen`; the channel is
  registered before the view starts the engine, so it exists when Dart's `main()`
  runs. No Flutter plugin and no extra Dart package were added: a window-management
  package would bring a plugin per platform (and its own window handling) for what
  is a single boolean. A Windows runner has to serve the same channel
 ; there is no `windows/`
  runner yet. Android and Android TV are always full screen: `supportsFullScreen`
  is false, the controller does nothing and Settings hides the row.
- **Startup.** `main()` applies the setting before `runApp`. The runner shows its
  window on the first Flutter frame, and a GTK window asked for full screen before
  it is mapped appears already full screen, so there is no visible resize.
- **Setting.** `SettingsService.startFullScreen`, saved by the Settings row
  "Start in full screen" (Select flips it, six buttons) and applied at once. With
  nothing saved, the installation's default decides: `PLAYHEADTV_FULLSCREEN`, else
  `start_fullscreen` in the config file, else on. A saved choice always wins over
  that default. The toggle is not widget state of the player page.

### Exit flow

There is no window chrome to rely on (a TV, a remote, a launcher that starts the
client and takes the screen back when it ends), so leaving is part of the app.

- **Flow.** `Back` (`InputAction.back`, Esc by default) on the mosaic opens a
  confirmation, "Exit Playhead TV?", with **Cancel** focused. `Back` again or
  Cancel closes it; Select on **Exit** leaves. The dialog is its own focus scope
  and needs only Up/Down/Left/Right/Select/Back. The player's `Back` chain is
  unchanged (close the EPG, hide an armed OSD, then the mosaic): exiting is always
  one more `Back` from the mosaic, never from the player. The mosaic's
  no-channels error screen handles `Back` the same way, so it is never a dead end.
- **Seam.** `AppExit` (`client/lib/services/app_exit.dart`) is the platform side:
  `exit(0)` on the desktop targets, `SystemNavigator.pop()` on Android (on Android
  TV the platform's own `Back` handling must be respected; whether the dialog
  stays there is open until that build exists). `AppExitController` is the policy:
  it runs the teardown steps in order, each bounded by a timeout and never allowed
  to stop the exit, then calls `terminate()`, once. Pages never call `exit()`;
  tests replace the interface (`appExitOverride`) so a suite never kills its own
  process.
- **Teardown order.** The mosaic's preview controller (taken out of the tree, then
  disposed through `serializeGlOp`), the display wake lock (`releaseScreenAwake`),
  then the syncer and the cache (`OfflineRuntime.reset`), then terminate. The
  player page is not alive at that point: its own dispose already released its
  controllers and the lock.
- **Not done.** No dedicated "quit" `InputAction` (an optional accelerator) and no
  "don't ask again" option.

### Mosaic geometry, navigation and UI scale

- **Frame.** `computeMosaicLayout` (`lib/mosaic_layout.dart`) sizes the 4x4 frame
  of 16:9 cells from the LayoutBuilder constraints: it fills the height minus a
  small safe margin (2% per side by default, `mosaic_safe_margin_percent` in the
  config file), centred, and falls back to fitting the width when the window is
  too narrow. Only layout boxes change on a resize; the preview controller and its
  texture are not recreated.
- **Navigation.** `mosaicNavigate` (`lib/mosaic_nav.dart`) is a pure function over
  the grid: each arrow goes to the nearest occupied tile that way (gap along the
  axis, then offset across it), the featured 2x2 block being one target. With
  nothing that way, a frame tile still enters the block when the block's centre
  lies that way, else the focus stays. The opposite arrow of the last move
  returns to its origin. The raw arrows stay outside `InputAction`.
- **UI scale.** `UiScale` (`lib/ui/ui_scale.dart`), provided once above the
  Navigator, is the shortest window side over 1080 (clamped 0.6 to 3), or a fixed
  step chosen in Settings ("Interface size"). Sizes are written as pixels at 1080p
  and read with `context.ui(px)`; stock Material pages and dialogs use
  `UiScaled`. Applied to every client screen: the mosaic, the OSD, the EPG
  overlay (rows, channel column, header and details panel; the 3h span still
  fills the width, so only the pixels-per-minute follow the width), the track
  picker, the update banner and changelog, Settings, Remap controls, Offline
  cache and the dialogs. The EPG's corner video thumbnail is scaled too
  (`_videoThumbnailRect`, computed from the same factor without subscribing the
  player page to it).
  A fixed Settings choice is capped by what fits the window
  (`maxFitUiScale`: the OSD needs `uiScaleFitHeight`/`uiScaleFitWidth` design
  pixels), so a large choice on a small window cannot overflow it; the
  automatic factor needs no cap.

### First-run screens on a phone

The eight phone slices (docs/PHONE_UI_PLAN.md) covered the home, the player, the
OSD, the guide, the overlays and the mosaics — every screen a *configured*
install reaches. A real phone found the gap on the way in: an install that has
not paired yet lands on `ServerUrlPage`, `PairingPage` or `SetupPage`, and those
three still drew a desktop form. Measured on a 393 dp phone, at the phone scale
the app had already chosen: the primary action ("Continue") was 65x35 dp, sized
by its own label, below the 48 dp a thumb needs; and `SetupPage`'s two buttons
in a `Row` were 135 px wider than the screen.

What they use now, and why it is not an `if (isPhone)` in each page:

- `FirstRunMetrics` (`lib/ui/first_run_metrics.dart`) says what a family needs:
  `tv` (intrinsic-width actions, no floor — a no-op, so the television layout is
  byte-identical) and `phone` (full-width actions, a 48 dp finger floor).
  `FirstRunAction` (`lib/widgets/first_run_action.dart`) is the phone half.
- The pages branch on that object for *layout* only; every size still comes from
  `context.ui(...)` through `UiScale`, and every text still scales.
- All three bodies are `FocusReveal` -> `SingleChildScrollView` -> `Column`, not
  `ListView`: same rule as the Settings pages (a row the viewport drops is a row
  the focus cannot come back to), and it is what makes the last action reachable
  in landscape with the keyboard up.
- `test/phone_first_run_test.dart` holds the contract: at 360x780 and 780x360
  nothing overflows, every control is at least a finger tall, every control can be
  scrolled into the window, and every action is at least 60% of the content width
  — plus one case asserting the *television* still gets intrinsic-width buttons on
  a single line, so the phone branch cannot quietly move it.

## Client form factors

Design plan for the phone UI: `PHONE_UI_PLAN.md`. What exists today is only the
seam, deliberately with no visual change.

- **Two layout families, one codebase.** `ClientFormFactor`
  (`lib/ui/form_factor.dart`) is `tv` or `phone`. `resolveFormFactor` takes three
  inputs: the window size, whether the platform reports a television (the
  `playheadtv/device` channel's `tv` flag, already asked once at boot for
  pairing), and whether the platform is a mobile operating system at all. A
  television is always `tv`. A non-mobile platform is always `tv`, whatever the
  window size, which is what keeps every desktop golden and suite (they run at
  320x180, 640x360, 800x450, 600x1000) on the 10-foot layout.
- **The television flag has two sources, and the size guard is only a
  fallback.** `MainActivity` answers `tv` from `UiModeManager.currentModeType`
  **or** the package features the store itself uses (`FEATURE_LEANBACK`,
  `FEATURE_TELEVISION`). Size alone cannot decide: measured on the lab's Android
  TV emulator, the screen is 1920x1080 at density 320, i.e. **960x540 dp** logical,
  and the image declares `android.hardware.touchscreen`, so both a "small screen"
  and a "has a touchscreen" test point at a handheld. A first cut that trusted
  the flag without a second source classified the lab TV as a phone, which
  `osd_test` caught by asking for the OSD's transport row. `phoneMaxShortestSide`
  is therefore 500 dp — above every phone (at most 430 dp) and below a 1080p box
  at 320 dpi — and the boot probe's timeout is 2 s, not 250 ms, because a timeout
  falls back to `tv` and a phone that times out gets a layout it cannot use. A
  720p box at 320 dpi is 640x360 dp and cannot be told from a phone by size at
  all: the flag has to answer, which is why it has two sources.
- **Resolved once, at boot, before the first page builds**, by
  `_InitialRouteResolverState` (one bounded platform-channel call, 250 ms cap,
  falling back to `tv`). A form factor is never re-derived from a resize: a
  resized desktop window must not change the app's personality, and a flip after
  the player page is up would rebuild the tree around a live video surface (the
  GL-texture gotcha). Rotation is safe by construction — it changes width and
  height, never the shortest side, so the answer does not move.
  `android:configChanges` already lists `orientation|screenSize`, so the Activity
  is not recreated and the channel keeps playing.
- **Provided above the UI scale**, because the scale depends on it:
  `ClientFormFactorHost` sits above `UiScaleHost` in `MaterialApp.builder`, both
  passing the Navigator through as `child:`, so a page below never rebuilds when
  either value changes.
- **Per-screen metrics instead of per-page branching.** The channel OSD reads
  `OsdMetrics` (`lib/ui/osd_metrics.dart`): 57 design units per family, with
  `OsdMetrics.tv` equal to the literals the widget used before the phone existed
  — the `osd_*.png` goldens match byte for byte, which is what makes the
  television claim falsifiable. The phone drops the transport row (five buttons
  disabled in every build) and the recent-channel strip, and applies
  `context.uiTouch(px, min: m.minTapTarget)` — floor 0 on a television, so no
  television pixel moves, and 48 dp on a phone. Measured on the phone metrics: the
  panel takes 18.5% of the height in portrait (360x780) and 40.2% in landscape
  (780x360), the smallest glyph drawn is 12.9 dp (the television's 11 px chip
  resolved to 6.6 dp at the old 0.6 scale), and every control is a 48 dp box.
  Tests: `client/test/phone_layout_golden_test.dart` (goldens in
  `client/test/goldens/phone_*.png`, deliberately a separate file from the
  frozen television ones).
- **The lab knows which layout a suite asserts.** `run_android_suites.sh` had
  one suite list plus `DESKTOP_ONLY`; it now has three sets, because a suite that
  asserts the ten-foot layout fails on a phone *for the reason it exists* and
  would read as a regression: `DESKTOP_ONLY` (needs the backend on the same
  machine), `TV_ONLY` (`mosaic_nav_scale_test`, `epg_layout_test`,
  `settings_navigation_visual_test`) and `PHONE_ONLY` (`phone_touch_test`,
  `phone_layout_test`). A general sweep skips the sets that do not match the
  profile; naming one by hand with the wrong profile is an error, not a
  four-minute run that ends in a red nobody should act on. `run_gui_suites.sh`
  keeps the matching `ANDROID_ONLY` for the desktop container.
  `tool/suite_map.tsv` gained the new files, so `--changed` reaches the phone
  suites from a change to `client/lib/ui/*`, `player_gestures.dart`,
  `system_bars.dart`, `dimmer_level_dialog.dart` or the mosaic.
- **An overlay a finger can open must be an overlay a finger can leave.** Four
  were reachable by tap and operable only by key, which is the one outcome the
  phone work must never ship, and all four are fixed the same way: the tap calls
  the method the key calls, never a second meaning.
  `TrackSelectorOverlay` picks an option on tap and closes on a tap outside the
  panel (the same `onSelect`/`onClose` Select and Back give); its rows are
  floored at a touch target through `context.uiTouch(px, min: minTapTarget)`, so
  the television's rows keep the height its frozen golden records.
  `DimmerLevelDialog` gained `-`/`+` beside the value plus Cancel/Save, inside
  `ExcludeFocus`: the dialog's own `Focus` owns every key, and a button that
  could take the focus would swallow Select. Its content is scrollable and the
  steps sit beside the value, because a 360 dp-tall window overflowed by 99 px
  with a second button row. `UpdateOverlay`'s banner and changelog are wrapped in
  plain `GestureDetector`s — no `FocusNode`, so "never takes focus" still holds —
  and the hint text now says "Tap or Select". The guide's cells got their `onTap`
  with `GuideMetrics`.
- **The mosaic becomes a column on a phone, and the two decisions behind it
  are pure modules.** `buildMosaicTiles` (`lib/mosaic_tiles.dart`) picks the
  tiles and their order for a `MosaicLayoutMode`: `ring` is the television's
  frame, capped at its twelve cells with a random fill of the empty ones;
  `list` is the phone's column, which holds **every** channel because a column
  has no cells to run out of, keeps the "most recently watched first" order (it
  means something there) and then falls back to channel number, which is how a
  viewer finds one. `mosaicMove` (`lib/mosaic_nav.dart`) is the arrow: the same
  two-dimensional walk in ring mode, one row at a time in list mode, where Left
  and Right mean nothing. `MosaicPage.build` picks a view and nothing else
  branches. The phone's `PhoneMosaicView`
  (`lib/widgets/phone_mosaic_view.dart`) draws the featured channel as a 16:9
  card capped at 40 % of the height — a landscape phone is 360 dp tall and a
  full-width card would be 438 — with "on now" from the cache under each name.
  Every row is built eagerly so every row can hold focus, and a move scrolls the
  focused row into view (`Scrollable.ensureVisible`) instead of hoping traversal
  does; a tap is the same outcome as Select on that row. The preview rules do not
  change at all: still one decorative `VideoPlayback`, still
  `mosaicPluginPreviewDwell` before a plugin resolve, still torn down on
  `hidden`/`paused`, still never holding the wake lock.
- **Touch is a layer, not a mode.** `PlayerGestureLayer`
  (`lib/widgets/player_gestures.dart`) is one `Positioned.fill` child of the
  player's Stack: above the video and the watermark, below the OSD, the spinner,
  the error banner and the guide, so every one of those keeps the taps it
  already had (a widget test pins that ordering). A tap toggles the OSD — it
  skips the "armed" state, which exists only to disambiguate Select from the
  arrows on a remote; a horizontal fling switches channel (left is next, the
  picture follows the finger) and only counts above `gestureFlingVelocity`
  (300 px/s), so a slip is not a channel change; a swipe up opens the guide; a
  swipe down does nothing, because a linear channel has nothing coherent to do
  with it. It is disabled while the guide or a track picker is open. Every
  callback calls the same page method the key path calls, which is what keeps
  touch additive for a television with a mouse rather than a second meaning.
  Rejected in the plan, for the record: double-tap to seek (a linear channel has
  no seek, and it collides with tap-to-show-OSD), edge swipes (Android reserves
  them), pull-to-refresh (only the syncer writes the cache).
- **The guide gets the same seam, with numbers that are not cosmetic.**
  `GuideMetrics` (`lib/ui/guide_metrics.dart`) carries the visible span, the
  channel column, the row height and the header height; `GuideMetrics.tv` is
  exactly what the overlay used as module constants, so the television baselines
  do not move. The grid is proportional, and that is what the phone broke: 220
  design px of channel column and a 3 h span put a half-hour program at **24 dp**
  on a 360 dp-wide screen — narrower than a touch target and too narrow for a
  title. `GuideMetrics.phone` is a 90 min span and a 72 px column, which brings
  the same cell to ~90 dp in portrait and ~230 dp in landscape, and drops the
  channel name (`number [logo]` only) because 72 px cannot hold both. The row
  height stays 84: a list of channels is no more compact on a phone.
- **Two touch paths in the guide.** A tap on a program focuses it and tunes when
  its displayed span covers now — the outcome Select on the focused cell has, so
  a drag never tunes by accident (a drag does not fire a tap). A horizontal drag
  on the grid moves the time axis one pixel per pixel, without animation while
  the finger is down, bounded like the arrows; it is a
  `HitTestBehavior.translucent` detector around the grid, so it catches a drag
  started on empty row space while the cells keep their own taps and the
  vertical `ListView` keeps vertical drags. Deliberately **not** in this slice:
  pinch to change the span (a new affordance, and a scale recognizer would fight
  the vertical list for the arena — it needs its own design), a Close button (the
  system Back gesture already closes the guide and is in the six-button set), and
  flipping `EpgThumbnailSetting`'s default per form factor (it stays a Settings
  row the viewer controls).
- **The player is immersive; the surfaces inside it read `viewPadding`.**
  `SystemBarsController` (`lib/services/system_bars.dart`) hides the bars with
  `immersiveSticky` while the player page is on screen and gives them back with
  `edgeToEdge` when it leaves — from `initState`, `didChangeAppLifecycleState`
  and `dispose`, never from `build`, and edge-triggered so a rebuild cannot
  churn the window. It has the same shape as `ScreenAwakeController` (a tiny
  platform interface, a tested policy, a `@visibleForTesting` override) and is a
  no-op off a mobile platform. Because the bars are hidden, `padding` reads
  zero on the player: the OSD panel, the OSD clock, the always-on clock and the
  guide's body read `MediaQuery.viewPaddingOf` instead, which still reports a
  cutout and a gesture handle. `android:windowLayoutInDisplayCutoutMode =
  shortEdges` lets the window use the notch area — a 16:9 picture letterboxes
  inside it anyway. Every inset is zero on a television and on the desktop, so
  the frozen goldens do not move; `phone_layout_golden_test.dart` pins a cutout
  case with a fake `viewPadding` instead. The mosaic is the one full-bleed
  screen with no AppBar, so it wraps its body in `SafeArea`.
- **Scale per family.** `autoUiScale` uses a 420 dp reference and a 0.85-1.3
  range for a phone (stock Material sizes; the 1080p reference would put every
  phone on the 0.6 floor and shrink body text to ~9.6 sp). `maxFitUiScale` uses
  the phone panel's own budget (`uiScaleFitHeightPhone`/`uiScaleFitWidthPhone`)
  — with the television budget, a plain 100% choice on a 360 dp screen was capped
  *below* the automatic factor, so the cap made it worse instead of preventing an
  overflow. `context.uiTouch(px)` applies Material's 48 dp minimum after the
  multiplication, for anything a finger must hit; a television measures nothing,
  so it keeps the plain `ui(px)`.
- **Not built yet, per the plan:** the per-screen metrics objects (`OsdMetrics`,
  `GuideMetrics`), the phone mosaic list view, the touch gesture layer, system
  bars and safe areas, and touch handlers on the overlays that only answer to a
  key today (the track picker, the dimmer level dialog, the update banner and the
  guide cells).

## Guide performance and start-up

Measured on an Android TV box (a low-end quad-core), where the frame budget is
tight: the guide took a single 115-170 ms frame to open and its Left/Right moves
dropped about 30% of frames, almost all of it widget *build* time (raster stayed at
11-18 ms). The cause was structural: the focus (`row`, `col`), the horizontal scroll
and the clock lived in `EpgOverlayState` and every change was a `setState`, which
rebuilt every visible row and every cell.

- **Focus, scroll and clock are notifiers, not state** (`lib/widgets/epg_overlay.dart`).
  The focus is one `ValueNotifier<(row, col)>`; a `_FocusSelector` rebuilds only the
  cells and rows whose look changes (the one that lost focus and the one that gained
  it). The horizontal scroll is an `AnimationController`; each row's cells sit on one
  layer that only gets a `Transform.translate`, so the scroll repaints the header and
  moves that layer and rebuilds no cell. The clock is a notifier read by the progress
  bars, the "now" marker, the header and the details panel; the grid itself is rebuilt
  only when a program starts or ends (`_nextBoundary`).
- **`setState` on the overlay is now rare**: a cache refresh, a window extension and a
  program going live. Never for a key press.
- **A held arrow keeps moving the focus** along the grid, one cell or row at a time
  (`_epgHoldRepeat` in `lib/main.dart`): key repeats are applied at most once per
  `epgHoldRepeatInterval` and the rest are dropped, so the pace is steady and nothing
  queues up after the key is released. It applies to the four arrows only; Select, Back and
  every other screen still ignore repeats.
- **Cells are drawn without a clip layer.** `Clip.antiAlias` costs an off-screen layer per
  cell on every repaint; the cells use `Clip.none` (the content sits inside the border).
  On the TV box this took the raster p95 from 14-19 ms to about 13 ms. Opening the guide
  still has one slow frame (build of the visible rows); building the rows in stages was
  tried and made the first frames worse (more, longer frames), so it was dropped.
- **Cell titles fill in after the first frame.** Laying out the ~100 titles was about half of
  the slow frame that opens the guide (measured by blanking them: worst frame ~130 ms to ~78 ms;
  with no grid at all ~50 ms). `_titleRows` grows by `_titleRowsPerFrame` rows per frame, nearest
  the focused row first, and any key press shows the rest. Only a title (a leaf) is deferred; a
  row or cell deferred the same way was tried and was worse (more frames, a longer build). The
  worst opening frame is now ~80 ms on the TV box. Tests that read titles wait a few frames after
  the guide loads.
- **The thumbnail is a setting** ("Show channel thumbnail in the guide", Settings, on by
  default; `EpgThumbnailSetting`, `lib/epg_thumbnail.dart`). Off: the video is not moved into the
  corner, the guide covers the whole screen with an opaque layer and the grid gets the height the
  thumbnail used. The video widgets are neither moved nor rebuilt either way (the GL texture
  gotcha). It made no difference to the slow opening frame, which is build time, not video.
- **Boot phases.** `BootTrace` (`lib/services/perf_trace.dart`) marks the phases of the
  start (`main`, `player_init`, `runtime_open`, `sync_done`/`warm_start`,
  `channel_on_air`) and logs them as one `boot` line. `PerfTrace` (opt-in, built with
  `--dart-define=PLAYHEADTV_PERF_TRACE=true`) records frame timings per window
  (`epg_open`, `epg_navigate`) on a device; `tool/mibox_perf.sh` drives it over adb.
- **A warm start does not wait for the network.** When the on-disk cache already
  holds upcoming programs for the channel to tune, `_bootstrapChannels` plays from
  it right away. The startup refresh still runs in the background; if it brings a
  different schedule, `_onCacheChange` applies it (reloading when what is on air
  changed, else in place), as it always did. Only a first run, or a channel with
  nothing cached, waits for the backend (`startupSyncWait`). The stream rules are
  waited for only when none were ever fetched. Before this, every start paid up to
  `startupSyncWait` (2 s) plus the rules request (up to 3 s) in front of the picture
  whenever the backend was slow or far.
- **The launch window is black** (`LaunchTheme`), like the app, so a TV does not show a
  white screen before the first Flutter frame.

## Headless GUI suites in containers

The GUI integration suites (`client/integration_test/`) need a desktop session and
are disturbed by anything that takes OS focus. `client/tool/run_gui_suites.sh`
runs each suite in its own container instead, so a contributor needs only Docker
and CI needs no display.

- One container per suite, `-j N` in parallel. The image
  (`client/test/docker/Dockerfile`: Ubuntu 24.04, pinned Flutter and Go, Mesa
  llvmpipe, ffmpeg, Xvfb, openbox, D-Bus) is built on first use; the Flutter
  version must match `.github/workflows/`, the Go version is read from
  `backend/go.mod`. `client/test/docker/entry.sh` gives each container a private
  HOME/XDG, an Xvfb of 1920x1080x24, a private D-Bus session and a window
  manager, and sets `CUDA_VISIBLE_DEVICES=-1`, `LIBVA_DRIVER_NAME=dummy`,
  `LIBGL_ALWAYS_SOFTWARE=1` and `LP_NUM_THREADS=2`. No audio device by default
  (a PulseAudio null sink made the suites less stable; `WITH_AUDIO=1` opts in).
- Each run uses a snapshot (`git archive HEAD`, or `--worktree`) copied inside
  the container, never the live checkout. `--worktree` takes the tracked files
  **and the new ones** (`git ls-files --others --exclude-standard`): it took only
  tracked files at first, so a file the tree had gained arrived as a missing
  import and a working change failed the build at 12 s, which reads like a
  product failure (hit on 2026-11-10, twice, before the cause was obvious).
  Containers are named, limited
  (2 CPUs, 4 GB, 1 GB shm) and removed on timeout. The pub and Go caches live in
  the named volumes `playheadtv-gui-pub` and `playheadtv-gui-go`
  (`run_gui_suites.sh --prune` removes them).
- The Mesa shader cache is a third named volume (`playheadtv-gui-mesa`,
  `MESA_SHADER_CACHE_DIR`) and must stay warm. libmdk reads a stale shader on
  its raster thread (`VideoShader::update`, called from `renderVideo`; the
  pointer it reads is freed memory), a race that a cold llvmpipe shader cache
  widens: with an empty cache the second app boot of a process segfaults at its
  first channel switch, almost every time, and with a warm one it does not. A
  fresh `$HOME` reproduces it on a plain host under `xvfb-run` as well, so it
  is not container specific; the Mesa version does not matter. The runner primes
  the volume once per image with a throwaway suite run. Never disable the shader
  cache for these runs.
- A failing suite is retried once only when its log matches an environmental
  signature (the app died or never started) and holds no assertion failure. The
  summary labels PASS, FLAKY (passed on the retry), FAIL and TIMEOUT; artifacts
  per suite and attempt are the log, the JSON results, the backend log, an
  optional screen recording (`-r`) and the host kernel's segfault lines when
  readable.
- `.github/workflows/gui-suites.yml` shards these desktop suites across a matrix
  on a hosted runner. It is manual or nightly (`workflow_dispatch` and a `schedule`
  entry), never on push or pull request, to save Actions minutes; the suites are
  run locally with `client/tool/run_gui_suites.sh`. A failing shard fails the
  workflow. It validates the desktop build only: the Android suites need the lab
  in `AGENTS.md` "Android validation" (emulator with `/dev/kvm`, host backend and
  media), so this workflow says nothing about Android.
- Limits: Docker does not validate real GPU rendering or the texture path,
  hardware decoders, input devices and remotes, window-manager fullscreen and
  focus, audio output, the `org.freedesktop.ScreenSaver` service or the
  launcher/AppImage packaging, and software decode changes frame timing.
  libmdk's raster thread can still crash under software GL (`did not complete`
  in the log, a `libmdk` segfault in the kernel log) when the shader cache is
  cold or the machine is overloaded; keep parallelism modest and keep a manual
  desktop pass.

## Releases and updates

The backend and the client are released separately, each from its own tag,
and talk through a small versioned contract. Operating steps are in
[`RELEASING.md`](RELEASING.md) and `deploy/README.md`; this section records
the design.

### Versioning

- **Backend release**: `vX.Y.Z` tags. The version is injected at link time into
  `internal/buildinfo.Version` (`-ldflags -X`; the Dockerfile takes `VERSION`
  and `COMMIT` build args) and is `dev` for any build without it.
- **Client release**: `client-vX.Y.Z` tags, a separate sequence in the same
  repository. The client knows its own version from
  `--dart-define=PLAYHEADTV_CLIENT_VERSION` (set by the packaging script);
  `pubspec.yaml`'s `version` is not used for releases.
- **API version**: an integer, `buildinfo.APIVersion`, bumped only for a change
  an older client cannot cope with (a removed or reshaped field or endpoint);
  additive changes keep it. It is served by the public `GET /version`
  (`{"version", "api_version", "commit"}`), next to `GET /healthz`.
- **Compatibility range**: the client declares
  `minSupportedApiVersion`..`maxSupportedApiVersion`
  (`client/lib/update/compat.dart`) and reads `GET /version` at start. Outside
  the range it shows a one-button dialog (Select dismisses it) saying which side
  to update; it never stops or crashes. A backend that is unreachable or
  predates `/version` is "unknown" and is never a reason to warn: the offline
  cache keeps working. Raise the max when a client release was verified against
  a newer backend API; raise the min when a client drops an old one.

### Backend image

A `vX.Y.Z` tag runs `.github/workflows/backend-release.yml`: build, vet and
test as a gate, then one multi-arch image (`linux/amd64`, `linux/arm64`) pushed
to `ghcr.io/<owner>/<repo>-backend` with the immutable tags `vX.Y.Z` and
`sha-<commit>`, a `latest` that follows the newest release, and a GitHub Release.
`vX.Y.Z` is the tag to pin when a deployment must not move; `latest` is for one
that follows releases, and it still only changes when the image is pulled and
the container recreated. The Dockerfile
cross-compiles with the build platform's Go toolchain (`GOOS`/`GOARCH` from
`TARGETOS`/`TARGETARCH`) instead of emulating the target, and the final stage is
distroless, which is why the server has a `healthcheck` subcommand (probes the
local `/healthz`) for container healthchecks.

Deployment is a Compose project synced by a GitOps manager (`deploy/`): the
image tag in the operator's repository is the single source of truth for what
runs. Migrations run at start, before the server listens, and are forward-only,
so a rollback after a migration is a restore of a backup taken before it.

### Client package

`client/tool/package_linux.sh` (the script CI runs on a `client-vX.Y.Z` tag,
`.github/workflows/client-release.yml`) builds the Linux x86_64 bundle with
Flutter, compiles the updater (`client/tool/updater.dart`) to a standalone
executable and writes `playheadtv-client-X.Y.Z-linux-x64.tar.gz` plus
`playheadtv-client-X.Y.Z-checksums.txt` (SHA-256, `sha256sum` format); the
workflow then adds the AppImage (see "AppImage") and signs the checksums file
(`.sig`, see "Signed releases"). The archive holds one directory: `bundle/` (the Flutter bundle, libmdk included,
and `playheadtv-updater`), the launcher `playheadtv`, `install.sh`, a desktop
entry template and `config.example.json`. Only linux/amd64 is built.

Installed layout, under one user-writable root (default
`~/.local/share/playheadtv`):

```
<root>/playheadtv          launcher (shared by all versions, not swapped)
<root>/config.json         settings: backend URL, device token, auto_update, ...
<root>/state.json          update state: pending marker, rolled-back versions
<root>/current  -> versions/X.Y.Z
<root>/previous -> versions/X.Y.Z
<root>/versions/X.Y.Z/     one extracted package
<root>/staging/            download and extraction scratch, wiped each run
```

Settings come from the config file (`PLAYHEADTV_CONFIG`, set by the launcher to
`<root>/config.json`; otherwise `$XDG_CONFIG_HOME/playheadtv/config.json`).
`AppConfig` resolves each value as: test override, then a non-empty
`--dart-define`, then the config file, then the default
(`client/lib/config/config_file.dart`), so a development build is unchanged.

### Crash supervision (desktop launcher)

A process killed by a signal cannot report it: no dialog, no log line, no
exception, nothing. The native video engine (`fvp`/libmdk) can die on a
teardown/create race (a use-after-free on its raster thread or on a reader
thread, reproduced on the desktop 2026-11-05 — see the `docs/ROADMAP.md` entry),
and from the user's side the app simply vanished.

So the launcher (`packaging/linux/playheadtv`) and the AppImage entry point
(`packaging/appimage/AppRun`) no longer `exec` the app: they run it as a child
and stay its parent. A zero exit is the user quitting and is passed straight
through. A non-zero exit that is not a signal is the app's own decision and is
passed through too. A *signal* death prints to stderr, shows a desktop
notification when `notify-send` exists, and starts the app again — at most
`PLAYHEADTV_CRASH_RESTARTS` times (default 3), `PLAYHEADTV_CRASH_DELAY` seconds
apart (default 3), so a crash on every start cannot become an invisible loop.
This is supervision, not app logic: every update decision still lives in the
Dart updater, and the app itself is unchanged by it. `client/tool/test_launcher.sh`
covers the matrix (restart, budget, non-signal exit, AppRun).

Nothing inside the app can do this job: recovering from a SIGSEGV needs a
process that is still alive.

### Headless auto-update

Where the logic lives, and why. The launcher script only does what must work
when the new app cannot: run the updater's two halves with a timeout and ignore
their status, fall back to `previous` if `current` is not runnable, then run the
app under a small supervisor. Everything that decides anything (check, compare,
download, verify, extract, swap, rollback) is Dart in `client/lib/update/`,
compiled to a standalone executable shipped inside the bundle
(`dart compile exe`, no display, no Flutter), so that

- it is unit-tested end to end against a fake release server and real
  `tar.gz` packages, instead of untestable shell;
- rollback of a version that crashes at launch still works, because the
  updater is a separate process from the app and runs before it;
- the app itself only has one job, to confirm it started healthy
  (`confirmPendingHealthy`, after 15 s of staying up, when started by the
  launcher). Updating a running Flutter app from inside is deliberately not
  done: it would need the app to restart itself and would leave nothing to
  recover a crashing start.

**The updater runs in two halves, and only the first one is on the boot path.**
`preflight` (steps 1 below) is local file work — a marker read and, at worst, a
symlink swap — and runs before the app. `prepare` (steps 2-4, which is where the
network is) is started by the launcher *after* the app is already starting, in
the background, and the launcher only waits for it when the app exits. Measured
reason: the release listing alone costs 0.65-0.9 s on a desktop connection
(dns 63 ms, connect 218 ms), against a whole in-app boot of ~1.0-1.3 s, and its
budget is 120 s — a slow or unreachable release server used to put two minutes
of black screen in front of the picture. A swap that finishes while the app runs
is simply what the next start runs; the app is never restarted behind the user's
back. The rollback half has to stay in front: it is what protects a start from a
version that cannot come up.

`playheadtv-updater preflight` / `prepare` on every start, in that order:

1. **Resolve a pending update** (`preflight`). After a swap, `state.json` holds a pending
   marker (new version, previous version, time). If it was confirmed, it is
   cleared and older versions are pruned. If it is still unconfirmed past the
   15 s window, the update is rolled back (`current` is pointed at the previous
   version) and that version is recorded as failed and not offered again (a
   newer release is). Inside the window nothing is done. A marker for a version
   that is not the installed one is stale and dropped. This runs even when
   auto-update is disabled.
2. **Check** the releases of the repository over HTTPS (`prepare`): the newest
   non-draft, non-prerelease `client-vX.Y.Z` release that has both assets of
   its own version (the repository also holds backend releases, so
   `/releases/latest` is not used; a release without a signature is still
   picked, so that step 3 refuses it visibly instead of it silently not
   existing). Stable is the only channel; any other value
   of `update_channel` logs and uses stable. `auto_update: false` in the config
   file, or `PLAYHEADTV_AUTO_UPDATE=0`, skips the check.
3. If the release is newer than `current`, **authenticate** it (see "Signed
   releases"): fetch the checksums file and its signature (assets must be HTTPS)
   and verify the signature, before anything else is downloaded. Then
   **download** the package, **verify** its SHA-256 against its line in the
   now-trusted checksums file, **extract** into `staging/` and check the bundle
   is complete.
4. **Swap**: move the directory to `versions/X.Y.Z`, write the pending marker,
   repoint `previous`, then repoint `current`, each by creating a temporary
   symlink and `rename(2)`-ing it over the old one (atomic: `current` always
   names a complete version). The launcher then starts the new version.

The flow is written once, over an update target (`client/lib/update/install_target.dart`):
`TarballTarget` is the layout above, `AppImageTarget` is "AppImage" below. A
target says which asset to download, how to put the verified file in place and
how to take it back; the check, authentication, hash, pending marker, health
confirmation and rollback bookkeeping are the same code for both.

Failure rules: every failure (offline, a bad answer, a failed download, a
missing or invalid signature, a checksum mismatch or a missing checksum line, a
bad package, a timeout of the whole step, an error while swapping) leaves the
installed version untouched, cleans `staging/`, and the launcher starts the
installed version. The whole step is bounded (120 s in the updater, 150 s by the
launcher's `timeout`), so a slow link delays a start by at most that and never
blocks it.

Known limits: the release listing is read unauthenticated, so the repository
must be public and the GitHub API rate limit (per IP) applies. A signature
proves the release was made by the holder of the signing key, not that the
release is free of bugs. The launcher and `install.sh` are not updated by the
updater (reinstall to change them). A user who quits within the first 15 s after
an update is treated as a crash and rolled back once. A download that cannot
finish within the budget is retried from the start at the next launch.

### Signed releases

A checksum protects against a corrupt download, not a tampered release: whoever
can edit a release can edit its checksums file too. So the **checksums file is
signed once** (Ed25519, one detached signature, the `.sig` asset: base64 of the
64-byte signature). The checksums file lists the SHA-256 of every downloadable
artifact (the tar.gz and the AppImage), so that one signature covers them all,
and the per-artifact hash then only has to match the signed list. The signature
is checked over the exact bytes fetched, before the artifact is downloaded
(`client/lib/update/signature.dart`, pure Dart via `package:cryptography`, so the
updater executable needs no native library).

- The client **pins public keys** in one place, `client/lib/update/pinned_keys.dart`
  (a list: a signature is accepted when any pinned key verifies it, which is how
  a new key is introduced before the old one is retired, see `RELEASING.md`). The
  private key exists only as the repository secret `CLIENT_SIGNING_KEY`; the
  release workflow signs with it, refuses to release when its public half is not
  pinned, and verifies the signature it just made.
- A missing, malformed or wrong-key signature, or a checksums file that no
  longer matches its signature, is a **refusal** (`signatureRejected`): logged,
  nothing downloaded, the installed version untouched. A missing signature is
  never a warning.
- A build that pins **no key** (the list ships empty until the maintainer pins
  the real one) refuses every update and says so, instead of silently skipping
  verification. The packaging script asks the compiled updater (`playheadtv-updater
  keys`) and refuses to package such a build, and the release workflow fails on
  it. The only escape is an explicit development switch at compile time
  (`--dart-define=PLAYHEADTV_ALLOW_UNSIGNED_UPDATES=true`, which the scripts only
  set for `ALLOW_UNSIGNED_DEV=1`); it applies only while no key is pinned, logs a
  warning on every use, and a build with a pinned key ignores it.
- Forks that sign their own releases pin their own key (edit the list, or
  `--dart-define=PLAYHEADTV_UPDATE_PUBLIC_KEYS=<key>[,<key>]`).

### AppImage

`client/tool/package_appimage.sh` builds `playheadtv-client-X.Y.Z-linux-x64.AppImage`
from the same Flutter bundle, **beside** the tar.gz (which stays the default
format), and appends its hash to the checksums file, so it is covered by the
signature. An AppImage is the type2 runtime (an ELF executable) with a squashfs
image appended; the script builds the squashfs with `mksquashfs` and concatenates
it behind a **pinned static runtime** (versioned release, SHA-256 verified before
use), instead of running `appimagetool`, which is itself an AppImage and would
bring in the host-FUSE requirement the format is meant to avoid. The static
runtime carries its own FUSE and squashfs support, so no `libfuse2` is needed on
the host (the kernel's FUSE and a `fusermount3`/`fusermount` helper still are, or
the file can be run with `--appimage-extract-and-run`).

Inside, `AppRun` plays the part of the launcher: it runs the same updater, with
`--appimage $APPIMAGE --version <VERSION file> --state-dir <dir>`, then starts
the app. The state (`state.json`, `update.log`) lives in
`${XDG_STATE_HOME:-~/.local/state}/playheadtv`; the config is looked up like for
any run without a launcher (`PLAYHEADTV_CONFIG`, else
`${XDG_CONFIG_HOME:-~/.config}/playheadtv/config.json`, created from the example
on first start).

The update replaces the AppImage **file**: the new one is downloaded and verified
into a staging directory next to it (same filesystem), the current file is
hard-linked to `<name>.previous` (the rollback copy), and the new file is renamed
over the AppImage path, atomically. The running process keeps the old inode, so
nothing breaks mid-session; `AppRun` then starts the file now at `$APPIMAGE`
(once, guarded by `PLAYHEADTV_REEXEC`, so a swap or a rollback takes effect at
once). The pending marker, the 15 s health confirmation (the app confirms in the
AppImage state file) and the rollback (the `.previous` file renamed back, the bad
version recorded as failed) are the tar.gz logic. Limits: a folder the user
cannot write to cannot be updated (the update fails early, before downloading),
and an AppImage whose own `AppRun` or updater cannot start at all has no outside
launcher to roll it back (the `.previous` file is still there to rename by hand).

### Update notices (banner and changelog)

Two small pieces of UI tell the user about updates. Both are best effort in the
offline-safe style of the client: an unreachable GitHub, a repository that
answers 404, a missing or corrupt state file are silent, and a development build
(`0.0.0-dev`) shows nothing.

- **Install kind** (`install_kind.dart`, a pure function over environment
  markers and the running binary's path): `tarball` (the launcher's
  `PLAYHEADTV_ROOT` contains the binary), `appImage` (`APPIMAGE`),
  `sandbox` (`FLATPAK_ID` or `SNAP`), `systemPackage` (a path under `/usr`,
  `/opt`, `/snap`), else `unknown` (a source build, a manual copy).
- **Banner** (`update_banner.dart`, `update_notices.dart`): the release listing
  the updater already uses feeds it, once per start. For a newer release the
  message follows the kind: tar.gz or AppImage with auto-update on, "installs
  automatically the next time Playhead TV starts" (or, when the updater already
  swapped a version in, "installed, restart"); auto-update off, how to apply it
  (turn `auto_update` on, or download the new package/AppImage); a system
  package, a sandbox and an unknown install, the matching generic instruction
  (the package manager, `flatpak update`, the releases page). A dismissed
  version is remembered (`SettingsService`) and not shown again until a newer
  release exists.
- **Changelog after an update**: when the updater swaps a version in it records
  the previous and new version and the release body in `state.json`
  (`last_update`). The running app shows it once (remembered per version) when
  its version is that one. The body is untrusted text: it is sanitised
  (control characters dropped, truncated to 4000 characters) and drawn as a
  plain `Text`, never parsed as markup or turned into links. A rolled back
  version's record is cleared. The release workflow generates the body
  (`client/tool/release_notes.sh`: the commits that touched `client/` or
  `shared/` since the previous `client-v*` tag, then the install text), so it is
  never empty.

The widget (`widgets/update_overlay.dart`) sits above the Navigator
(`MaterialApp.builder`), is driven by a `ValueNotifier`, and never rebuilds a
page. It does not take focus: while visible, an early key handler claims only
Select (dismiss; for a banner, remember the version), Back (close; a banner
comes back next start) and, for a changelog that overflows, raw Up/Down to
scroll; every other key reaches the page below untouched, and playback is never
interrupted. Unattended, the banner leaves after 25 s (and is not remembered as
dismissed), the changelog after 90 s (it counts as shown from the moment it
appears).

## Device pairing

An unconfigured client has no credentials, so it pairs with a short-lived PIN
instead of asking the user to type a server address, a device token and a
media-server key.

1. The client calls `POST /pair` (public) with its display name and platform and
   receives an 8-character PIN (shown `XXXX-XXXX`, from an alphabet without
   look-alike characters), a 64-hex poll secret and a 10 minute lifetime. The PIN
   and the poll secret are stored only as hashes; the poll secret never leaves
   the device.
2. The client shows the PIN and a QR code of `<server>/pin?code=<PIN>`.
3. On a phone or computer the user opens `GET /pin`: a tiny server-rendered form
   (no JavaScript, no external requests; `Content-Security-Policy`,
   `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, `Cache-Control:
   no-store`). The user types the PIN (prefilled from the QR), signs in with an
   `app_user` e-mail and password and approves. Only while the server has no
   media-server configuration yet does the form also ask for the Jellyfin
   address and API key.
4. The client polls `POST /pair/poll` with the poll secret. Once approved, that
   one response carries the device token and the Jellyfin address and key, and
   the pairing row is deleted in the same transaction. A second poll is a 404.

The device token is minted at poll time, with the same code path as device
registration, and is never stored in clear: nothing in the database can hand the
token out twice. The Jellyfin address and key reuse the encrypted `media_source`
row; `GET/PUT /server-config/jellyfin` (and the matching MCP tools) read and
change them, and a read never returns the key, only whether one is set and its
last characters. Today there is one such configuration for the single tenant;
per-profile or per-user media-server tokens would hang off the same tenant key.

Abuse limits: a per-address rate limit on `POST /pair`, `POST /pair/poll` and
`POST /pin/approve`, a cap on pending pairings, a per-pairing failed-approval
counter that deletes the pairing after a few wrong tries, constant-time
comparisons, generic error messages that do not reveal whether a PIN or an
e-mail exists, and lazy cleanup of expired rows. None of the secrets is logged
or put in a URL.

Client side: the app is built with an optional official server address
(`--dart-define=PLAYHEADTV_OFFICIAL_URL`). When the connection is not configured
it opens the pairing screen for that server, or for one the user chose earlier;
without either it first asks for a server address. "Select another server" on the
pairing screen and Settings > Server reach the address screen, and "Enter details
manually" keeps the old manual setup. The decision is a pure function in
`client/lib/services/pairing_boot.dart`.

## Status

The schema, domain types, SQLite storage layer, REST API, EPG materializer,
auth, Jellyfin pull sync, MCP server and admin web UI are implemented; see
[`ROADMAP.md`](ROADMAP.md) for the current checklist and what is still open
(D1 storage, Plex/local-folder sources, push-sync relay, Android/Android TV
builds).
