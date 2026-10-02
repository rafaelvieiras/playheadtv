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

`media_source.last_synced_at` is surfaced in the web UI (both pull and push)
so staleness is visible, not hidden.

## Scheduling model

Loosely inspired by ErsatzTV's Block Schedule + filler model
(PreRoll/MidRoll/PostRoll/Tail/Fallback), chosen over Tunarr's flat time
slots for finer control (a commercial only mid-block, a bumper only at the
edges):

- **`block`**: a reusable programming template — an ordered list of
  `catalog_item`s (`block_item`). A block's `block_item` rows are either
  curated by hand (the default, via `POST`/`DELETE /blocks/{id}/items`) or,
  when `block.criteria` is set (the same JSON `{kind, genres, year_from,
  year_to}` shape as `filler_pool.criteria`), owned entirely by
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
- **`filler_pool`**: a named collection of filler content (commercials,
  bumpers, station IDs). **`filler_rule`** scopes a filler kind
  (preroll/midroll/postroll/tail/fallback) to a channel or a single block,
  drawing from a pool with a selection strategy (sequential/random/weighted).
  A pool's `filler_pool_item` rows are either curated by hand (the default,
  via `POST`/`DELETE /filler-pools/{id}/items`) or, when `filler_pool.criteria`
  is set (a JSON `{kind, genres, year_from, year_to}` blob), owned entirely by
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

`POST /channels/{id}/reset-epg` force-resets one channel: it discards the
materialized future and the entry on the air, clears the persisted rotation
(it restarts from the first block item) and regenerates synchronously. It is
the only way a change inside the freeze margin takes effect early. The web UI
exposes it on the channel list behind a confirmation dialog.

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
