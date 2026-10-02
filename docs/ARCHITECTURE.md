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

## Media sources

A `media_source` is a Jellyfin/Plex/local-folder library. Only `jellyfin` is
implemented in v1; the `type` column and `catalog_item` cache are already
source-agnostic so adding Plex/local-folder later doesn't touch scheduling
tables at all. **A channel can mix items from different media sources in the
same schedule** — every scheduling table (`block_item`, `filler_pool_item`)
references `catalog_item.id`, never a source's native ID.

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
paginated, authenticated via the `X-Emby-Token` header with the source's
decrypted credential), maps `Movie`/`Episode` items into `catalog_item`
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
Deliberately excluded from the MCP surface in v1, same rationale as
`catalog-sync`: it's data sync, not authoring.

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

A query is a versioned JSON condition tree, the one stored form of
`block.criteria`, `filler_pool.criteria` and the spec of a `criteria` block
item: `{"version": 1, ...}` plus exactly one of `all: [node]`, `any: [node]`,
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
`{query, playback_order, seed, amount}`) live in manual blocks next to catalog
and series items. A visit airs `amount` matches (`{mode: count, count: N}`,
required, or `{mode: all}`, which must be chosen explicitly: every match once),
then the rotation moves on. Orders: `title` (the default), `release_date`,
`random`, `shuffle`. The materializer walks them with the same cursor as a
series (`seriesRun`: `series_cursor`, `epg_entry.source_block_item_id` and
`series_cycle`, seed mixed with the channel id), the matches playing the part
of episodes (items with a non-positive duration are left out), so `all` is "the
rest of the playback cycle". The query is re-evaluated on every materializer
run. Changing the order restarts the item; changing the query or amount keeps
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
closed the previous round). `on_exhaustion` is `restart` (default) or `drop`:
a series that has run out waits (or leaves) while the others carry on, and
once every episode of the cycle has aired the cycle ends. `restart` then opens
the next cycle at once, so every series starts over and the rounds begin
again; `drop` hands over to the block's own repeat behavior: a dynamic block
loops (the same result), while a `count` visit of a criteria item stops at the
end of the cycle instead of rolling into the next one. The walk is the series
cursor above with one more persisted column, `epg_entry.series_round`, so a
catalog change mid-run behaves sensibly: a series gained joins when its turn in
the round comes up, a removed one leaves with no hole.

A dynamic block's order is `block.playback_order` (`title`, the default and
exactly the old behavior, or `interleaved`) with `interleave_spec` (JSON) and a
`playback_seed` (migration `0013`; PUT is a full replace, omitted means
`title`). An interleaved dynamic block is walked as one stand-in criteria item
that airs until the slot ends; since its `block_item` rows are recreated on
every catalog change, its cursor is `block_cursor` (per channel and block) and
its entries record the block id in `source_block_item_id`. Changing the order,
`per_round` or `series_order` restarts the cursor.

## Scheduling model

Loosely inspired by ErsatzTV's Block Schedule + filler model
(PreRoll/MidRoll/PostRoll/Tail/Fallback), chosen over Tunarr's flat time
slots for finer control (a commercial only mid-block, a bumper only at the
edges):

- **`block`**: a reusable programming template — an ordered list of
  `catalog_item`s (`block_item`). A block's `block_item` rows are either
  curated by hand (the default, via `POST`/`PUT`/`DELETE /blocks/{id}/items`) or,
  when `block.criteria` is set (a condition tree, see "Catalog queries", the
  same as `filler_pool.criteria`), owned entirely by
  `catalogsync.ResolveDynamicBlocks`: it recomputes that block's membership
  against the current `catalog_item` table every time the catalog changes,
  sharing the matching query with `ResolveDynamicPools` (both call the same
  `matchCriteria` helper). This is how a real 24/7 genre channel (e.g.
  "Comedy Movies") gets built: a dynamic `block` scheduled via one
  `schedule_item`, not a `filler_pool`/`filler_rule` misuse — `filler_pool`
  stays reserved for commercials/bumpers/station IDs (see below). The
  materializer treats a dynamic block differently from a manual one: instead
  of airing its item list once per `schedule_item` occurrence, it loops
  that list continuously (`internal/materializer/expand.go`'s
  `fillLoopingBlock`) to fill the entire window up to the next occurrence
  (or the materialized window's end for the last one), and every entry it
  emits is tagged `kind=program` — real programming, not filler. A dynamic
  block's `preroll` filler rule still applies once, at the top of the
  occurrence; `midroll`/`postroll`/`tail` don't apply to it, since an
  endless loop has no single "end" to hang them off. A manual block is
  unaffected: it still airs its list once and stops, exactly as before.
- **Series block items** (migration `0010`): `block_item.item_type` is
  `catalog` (one catalog item, as before, optional `trim_duration_ms`) or
  `series`. Type-specific data lives in the JSON `spec` column, so further
  kinds add a type value and a spec shape. A series spec is
  `{media_source_id, series_external_id?, series_title?, playback_order, seed,
  slice}`: the series is its media source plus external id (else title), `seed`
  makes shuffle/random reproducible and is never served, `slice` is reserved
  for slicing (empty; the API rejects a non-null value). Series items live in
  manual blocks only (filler pools keep single items or criteria) and take no
  trim: every episode plays in full. `GET /series?q=` lists the catalog's
  series with episode count and total duration; block item responses carry a
  `series` view with the live episode count and total duration.
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
  starts afresh (full amount) at the next gap. A manual block plays its list
  once, a `count`/`series_end` visit handing over to the next item (no midroll
  follows a series visit). In a continuous block the entry completing a visit
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
  skipped without using up episodes. One cap (`maxSeriesEntries`) bounds a
  single expansion.
- **`filler_pool`**: a named collection of filler content (commercials,
  bumpers, station IDs). **`filler_rule`** scopes a filler kind
  (preroll/midroll/postroll/tail/fallback) to a channel or a single block,
  drawing from a pool with a selection strategy (sequential/random/weighted).
  A pool's `filler_pool_item` rows are either curated by hand (the default,
  via `POST`/`PUT`/`DELETE /filler-pools/{id}/items`) or, when `filler_pool.criteria`
  is set (a condition tree, see "Catalog queries"), owned entirely by
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
- **`schedule`** / **`schedule_item`** / **`channel.schedule_id`** (issue #7,
  the ErsatzTV Schedule/Playout split — see
  `docs/competitive-analysis/ersatztv.md` section 4): `schedule` is a
  reusable, named group of rules, not tied to any channel. `schedule_item` is
  one rule inside it — when a block airs, either weekly recurring
  (`day_of_week` + `start_time_local`, resolved against `channel.timezone`)
  or a one-off absolute `start_at` (this is `schedule_entry`'s old shape,
  before this split, minus the `channel_id` it used to carry directly).
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

None of the above is ever read directly by a client. It's authoring input to
EPG materialization.

## EPG materialization

A channel's assigned `schedule` (its `schedule_item` rows) + `block`/
`block_item` + `filler_rule`/`filler_pool_item` are expanded asynchronously
into flat `epg_entry` rows — concrete
`catalog_item` + `start_at`/`end_at` pairs — by a background process:

- Triggered immediately on any write to a scheduling table (schedule
  change → regenerate affected channels).
- Rolled forward on a timer so a window (e.g. 48h) always stays materialized
  ahead of "now", independent of writes.
- Implemented as an in-process goroutine/queue in the Go server — not a
  Cloudflare Cron/Queue, since the backend is a normal long-running process
  either way (self-hosted or cloud-sync), not a Worker.

This is the one non-trivial computation in the whole backend, and it never
runs on a request path.

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
construction.

### Continuous schedule items and grid stability

A `schedule_item` with `recurrence_type = continuous` has no time: it is the
base layer of a channel, airing in every span the timed items leave (and
rotating through its block, whole programs only, with a residual too short for
the next program handed to the channel's fallback rule). At most one per
schedule, enforced by the API (`409` otherwise). A timed item still starts at
its nominal time; the continuous item resumes right after it where its
rotation left off. A continuous item has no preroll/midroll/postroll/tail.

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
  (same validation as `blocks.criteria`/`filler_pool.criteria`): returns
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
  differ from a later real reset.
- MCP equivalents: `update_block_item`, `update_filler_pool_item`,
  `preview_criteria`, `preview_channel_grid_reset`.

### API

- `GET /epg`: the full materialized schedule for every channel in the
  tenant, over a configurable window (default 24h). This is the "XMLTV" of
  the project, as JSON — one bulk fetch instead of a client polling
  per-channel or per-minute.
- `GET /epg/meta`: `{revision, valid_until, freeze_margin_ms}` only — cheap enough to poll
  often. Clients cache the full `/epg` response locally and only refetch it
  when `revision` changes or `valid_until` is close.

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

## Status

The schema, domain types, SQLite storage layer, REST API, EPG materializer,
auth, Jellyfin pull sync, MCP server and admin web UI are implemented; see
[`ROADMAP.md`](ROADMAP.md) for the current checklist and what is still open
(D1 storage, Plex/local-folder sources, push-sync relay, Android/Android TV
builds).
