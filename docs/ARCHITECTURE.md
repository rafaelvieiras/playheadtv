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
  (drops season 0; specials stay included unless this is set), `episode_from`..
  `episode_to`, `skip_first`, `skip_last`, `first` or `last` (never both),
  `sample`. They apply in that order, each to what the previous left: season
  filter, specials, episode range, skip, first/last, sample. Season and episode
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
  ending at 0 alone), and a slice that currently matches no episode. An empty
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
  drawing from a pool with a selection strategy (sequential/random/weighted).
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
  tenant, over a configurable window (default 24h). This is the "XMLTV" of
  the project, as JSON — one bulk fetch instead of a client polling
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
    length; `end_at - start_at` is what is scheduled) and the image tags
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
  runs on the wall clock across program plus filler), "Up next" is the next
  visible program, and it only re-announces when the attributed program
  changes, so a filler of the same program (or another clip of a tail) never
  pops it again. A `fallback` is described as itself, and so is a hidden entry
  nothing could be attributed to. When the filler already on air at tune-in
  belongs to a program that ended before the first fetched entry, the client
  fetches the few hours before it to find that program. A long `tail` is
  attributed to the previous program for its whole length, so the OSD keeps
  naming that program until the next one starts. The web UI's "Show filler" toggle (UI state only) draws every entry
  at its real span instead. The rules are one pure function per platform
  (`client/lib/epg_guide.dart`, `webui/src/lib/guideDisplay.ts`) with the
  same unit tests. There is no per-channel or per-block setting.
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
