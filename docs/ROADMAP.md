# Roadmap

Design rationale lives in [`ARCHITECTURE.md`](ARCHITECTURE.md); this file is the
actionable checklist.

## Phase 0 — Technical spike (done)

- [x] Client-side channel assembly validated end-to-end: preload + manual
      crossfade (opacity/volume) between two `VideoPlayerController` instances,
      against a real Jellyfin server, GPU-accelerated decode (CUDA/NVDEC).
      `client/lib/main.dart`.
- [x] Video engine: `fvp` (libmdk), swapped from `media_kit` after an unresolved
      upstream Linux rendering bug. TODO: document the fvp migration story
      (why `media_kit` was dropped) in a client section of `ARCHITECTURE.md`.
- [x] Backend schema + domain types: `backend/migrations/0001_init.sql`,
      `backend/internal/domain/`, `backend/internal/storage/db.go` (interface
      only, no implementation yet).

## Phase 1 — Backend core (next up)

- [x] `storage.DB` implementations: `sqlite.go` (local file, self-hosted
      default) — `backend/internal/storage/sqlite.go`, driver
      `modernc.org/sqlite` (pure Go, no CGO, so the binary cross-compiles and
      containerizes trivially). `d1.go` (Cloudflare D1 HTTP query API,
      cloud-sync tier) deliberately deferred until that tier is actually
      worked on — it needs a Cloudflare account/API token we don't have yet.
- [x] Migration runner (apply `migrations/*.sql` on startup, track applied
      versions) — `backend/internal/storage/migrate.go`, storage-agnostic
      (talks only to the `storage.DB` interface, so it'll work unchanged once
      `d1.go` exists).
- [x] Minimal runnable entrypoint (`backend/cmd/server/main.go`): opens the
      DB, runs migrations, serves `GET /healthz`. No business routes yet —
      that's the next bullet below.
- [x] Dockerized: `backend/Dockerfile` (multi-stage, static binary, final
      image `distroless/static`) + root `docker-compose.yml` (self-hosted
      quick start, persists the SQLite file in a named volume). Default port
      `9394`.
- [x] REST API (`backend/internal/api`; every handler still runs against one
      implicit `DefaultTenantID` — Auth below only answers "who's calling",
      not multi-tenant):
  - [x] `channel` CRUD
  - [x] `media_source` CRUD + connectivity probe (decides `sync_mode`
        pull/push automatically) + `POST /media-sources/{id}/catalog-sync`
        (push ingestion)
  - [x] `catalog_item` search/browse (backs the authoring UI's media picker)
  - [x] `block` / `block_item` CRUD
  - [x] `filler_pool` / `filler_pool_item` / `filler_rule` CRUD
  - [x] `schedule_entry` CRUD
  - [x] `GET /epg`, `GET /epg/meta` — now backed by a real materializer, see
        the bullet right below.
- [x] EPG materializer: in-process async worker (`backend/internal/materializer`),
      triggered on writes to `schedule_entry`, `block_item` (add/delete —
      not a `block`'s own name/description), `filler_rule`, and
      `filler_pool_item` (add/delete — not a `filler_pool`'s own name), plus
      a 5-minute rolling timer keeping a window (default 48h) materialized
      ahead of "now". Writes `epg_entry`, bumps `epg_revision`. Known v1
      limitations, accepted rather than deferred: each run fully regenerates
      the whole future window rather than diffing it incrementally; the
      `sequential` filler selection cursor lives only in memory for the
      duration of one run and resets on the next one or on restart;
      overlapping schedule_entry occurrences are resolved by pushing the
      later one to start right after the earlier one's real (filler-inclusive)
      end, never by cutting either short; and the write-triggered path only
      covers the four tables above — a change to `channel`, `media_source`,
      or a `block`/`filler_pool` rename only takes effect on the next
      periodic tick, not instantly.
- [x] Auth: `app_user` sessions (bcrypt password hash, opaque session token,
      SHA-256'd at rest) and `device` API tokens (same token scheme, one
      active token per device), `backend/internal/api/auth.go`. A single
      `RequireAuth` middleware wraps the whole mux (not per-route) with three
      tiers: always-open (`/healthz`, `POST /app-users`, `POST /sessions`),
      device-or-session (GET-only reads a playback client needs: `/epg`,
      `/epg/meta`, `/channels`, `/channels/{id}`, `/catalog-items/{id}` — bare
      `GET /catalog-items` stays session-only, it's an authoring search
      surface), and session-only (everything else). `POST /app-users` only
      ever succeeds once per tenant (no invite/registration flow yet — still
      single-user per the "no real multi-tenant" note above). Login/lookup
      failures are indistinguishable (generic "invalid email or password" /
      "unauthorized").
- [x] Device pairing: a client with no configuration shows a PIN and QR code,
      the user approves it at `GET /pin` and the device receives its token plus
      the Jellyfin address and key (`POST /pair`, `/pair/poll`, `/pin/approve`;
      `GET/PUT /server-config/jellyfin`). `docs/ARCHITECTURE.md` "Device pairing".
- [x] Password recovery: `server reset-password` mints a single-use token
      (hash + expiry in `password_reset`), `POST /password-resets/consume` and
      the web UI page `/admin/reset-password` set the new password and end the
      user's sessions (device tokens kept). `docs/ARCHITECTURE.md` "Password
      recovery"; runbook in `deploy/README.md`. Not done: a signed-in
      `PUT /me/password`.
- [x] Encrypt `media_source.credential_cipher` at rest: AES-256-GCM,
      `backend/internal/crypto` (pure stdlib, nonce prefixed to the
      ciphertext). Key resolution order: `PLAYHEADTV_ENCRYPTION_KEY` (base64,
      32 bytes) env var, then a persisted key file (`encryption.key`, next to
      the SQLite file, `0600`), then auto-generate-and-persist with a loud
      backup warning logged. See README.md "Encryption key".
- [x] Real Jellyfin pull sync: `backend/internal/pullsync` (background
      worker mirroring `internal/materializer`'s Trigger/Start/Run shape,
      plus a synchronous `SyncOne` for the manual path), `POST
      /media-sources/{id}/pull-sync`. Queries `GET {base_url}/Items`
      (`Authorization: MediaBrowser Token="<key>"`, paginated; works on
      Jellyfin 10.10+ and 12.x, see `docs/ARCHITECTURE.md` "Jellyfin
      authentication") directly, maps Movie/Episode items
      into `catalog_item`, and deletes anything that disappeared from
      Jellyfin. The upsert/delete/last_synced_at SQL is shared with the push
      flow via `backend/internal/catalogsync`, not duplicated. Runs on a
      fixed 15-minute periodic tick plus an opportunistic trigger right
      after `POST`/`PUT /media-sources` results in `sync_mode: pull` for a
      jellyfin source, so a newly-registered library populates almost
      immediately without blocking that request. Deliberately excluded from
      the MCP surface (same exclusion rationale as `catalog-sync`). Known v1
      limitation: a source whose Jellyfin later goes unreachable keeps
      retrying quietly (logged, not surfaced) rather than automatically
      flipping back to `sync_mode: push` — re-probing on sync failure is
      accepted backlog, not this pass's scope.
      Verified: `go build ./... && go vet ./...` clean; `go test ./...`
      passes, including new tests in `internal/pullsync` (item mapping,
      pagination against a fake Jellyfin `/Items` serving a real JSON
      payload — not just a status code, the first fake in the repo to need
      that — credential decrypt-then-header round trip, per-source failure
      isolation) and `internal/api` (`POST .../pull-sync` rejects the wrong
      mode with 409, succeeds end to end against a fake Jellyfin and
      populates `catalog_item`); the pre-existing push `catalog-sync` tests
      pass unchanged. Web UI: `MediaSourcesPage.tsx` gained a "Last
      synced" column and a "Sync now" button (jellyfin+pull sources only), `npx tsc --noEmit && npm run build` clean.
- [x] Pull sync captures `catalog_item.genres`/`production_year`: Movie reads
      both fields directly off the Jellyfin item; Episode inherits genre from
      its parent series (own `Genres` always empty on Jellyfin) via one
      `GET /Items/{seriesId}` per unique series per sync, with the `userId`
      that endpoint requires resolved once from `GET /Users`.
- [x] Catalog metadata sync (season/episode numbers, overview, ratings,
      people, studios, tags, languages, chapters, art references): see
      `docs/ARCHITECTURE.md` "Catalog metadata". `GET /catalog-items/{id}` and
      MCP `get_catalog_item` carry the full detail, the list endpoint and
      `search_catalog` the compact form; the web UI shows `S01E03` next to
      the series name.
- [x] Series block items: a manual block can hold a whole series with a
      playback order (`original`, `random`, `shuffle` without repeats,
      `release_date`); see `docs/ARCHITECTURE.md` "Series block items".
      `POST/PUT/DELETE /blocks/{id}/items`, `GET /series`, MCP
      `add_block_item` (with `series`), `delete_block_item`, `search_series`,
      block editor "Add a series". Not yet: series items in
      filler pools are not decided.
- [x] Composable catalog queries, stage 1: `block.criteria` and
      `filler_pool.criteria` hold a versioned JSON condition tree
      (`all`/`any`/`not` over field conditions, strict validation) instead of
      the fixed `{kind, genres, year_from, year_to}` shape; a third block item
      type, `criteria`, plays N matches (or all) of a query inside a manual
      block. `POST /catalog-items/preview-criteria` takes a tree, startup
      migration converts old criteria and holds back any whose matched set
      would change (`GET /criteria-migration-review`), web UI query builder,
      MCP `preview_criteria`/`add_block_item`/`update_block_item`. See
      `docs/ARCHITECTURE.md` "Catalog queries". Not yet: a text syntax.
- [x] Composable catalog queries, stage 2: the `collection` entity
      (migration `0014`). A collection holds only a name, a description and the
      query; dynamic blocks, dynamic filler pools and criteria items reference
      it by id, playback settings stay on them. Startup migration
      (`MigrateToCollections`) moves every owner into a collection (identical
      trees share one, before/after matched sets compared, mismatches held in
      the review list); the old `criteria` columns are deprecated and kept. REST
      (`/collections`, usage, delete 409), MCP, a Collections page and pickers
      in the editors; "apply proposal" creates a collection. See
      `docs/ARCHITECTURE.md` "Collections". Not yet: dropping the deprecated
      `criteria` columns (a later version).
- [x] Interleaved-by-series playback order (round-robin across series) for
      criteria items and for dynamic blocks (`block.playback_order`,
      migration `0013`; default `title` keeps the old looped-by-title
      behavior), with `per_round`, `on_exhaustion` (`restart`: each series loops on
      its own while longer ones continue; `drop`: a finished series leaves for
      good, then the usual end-of-list behavior) and `series_order`; REST, MCP (`create_block`/`update_block`), block editor.
      Query fields `actor` and `director` next to `person`.
- [x] Large-tenant robustness: startup backfills (`search_text`,
      `overview_norm`, genre links) in batched transactions with their own
      budget, progress logs and resumability; series cap warning, cap no longer
      consumed by skipped/past slots, O(1) series steps and transactional
      per-channel writes with a per-channel budget; a criteria/series item that
      fails to evaluate is logged as an error and reported by
      `GET /channels/{id}/epg-warnings` (guide warning, MCP preview). Still
      open (tracked separately): query SQL cost at 200k items, FTS5 search,
      push-sync throughput, materializer load strategy, shuffle order being
      O(n) per step.
- [x] Series item amount per visit (`amount: {mode, count}` in the item's
      spec, REST/MCP/block editor): a number of episodes (default 1), until
      the end of the slot, or until the series ends (rest of the cycle in
      progress). Items stored earlier read as "until the end of the slot".
- [x] Midroll for series episodes: episodes from series items, criteria items
      and interleaved dynamic blocks get the same midroll breaks as catalog
      items of a manual block (same rule resolution and pool pick), between
      episodes and between a visit and the next item; also in continuous
      channels. Not yet: breaks inside a single long episode (midroll has no
      such mode for catalog items either).
- [x] Dynamic `filler_pool`s: an optional `filler_pool.criteria`
      (`{kind, genres, year_from, year_to}`) makes `catalogsync.ResolveDynamicPools`
      own that pool's `filler_pool_item` rows, recomputed after every catalog
      sync (push's `catalog-sync` handler and pull's `SyncOne`, plus once
      synchronously right after the criteria is written via
      `POST`/`PUT /filler-pools`) — no more hand-inserting items one at a
      time to keep a "genre X" pool current. `POST`/`DELETE .../items` now
      returns 409 on a dynamic pool (items are computer-managed). Also fixed a
      latent bug this surfaced: `catalogsync.Delete` now also removes any
      `filler_pool_item` referencing the deleted `catalog_item` first, since
      that FK has no `ON DELETE CASCADE` and dynamic pools make "an item
      referenced by a filler_pool_item disappears from the catalog" the
      common case instead of a rare hand-curation mistake.
- [x] Dynamic `block`s: the same `criteria` concept ported to `block`
      (migration `0004_block_dynamic_criteria.sql`), so a 24/7 genre channel
      is real programming (`kind=program`) instead of a `filler_rule`
      `kind=fallback` misuse — `filler_pool` stays reserved for
      commercials/bumpers/station IDs. `catalogsync.ResolveDynamicBlocks`
      owns a dynamic block's `block_item` rows the same way
      `ResolveDynamicPools` owns a dynamic pool's, sharing the matching query
      (`matchCriteria`) between the two; `catalogsync.ResolveDynamic` calls
      both and is what `pullsync.Syncer.SyncOne`/push's `catalog-sync`
      handler now call instead of `ResolveDynamicPools` alone.
      `POST`/`PUT /blocks` accept the same `criteria` shape as
      `/filler-pools` (reuses `fillerPoolCriteriaRequest`/
      `validatePoolCriteria` as-is), and `POST`/`DELETE /blocks/{id}/items`
      return 409 on a dynamic block, same as filler pools.
      `internal/materializer/expand.go` gained `fillLoopingBlock`: when a
      channel's block has non-NULL `criteria`
      (`channelInput.DynamicBlockIDs`), `expandChannel` loops that block's
      item list continuously to fill the whole window up to the next
      `schedule_entry` occurrence (or the materialization window's end for
      the last one), tagging every entry `kind=program` (the filler rules
      now apply per program, see the item on endless blocks below).
      `catalogsync.Delete` also removes any orphaned `block_item`
      before deleting a `catalog_item`, for the same FK reason as
      `filler_pool_item`.
- [x] Issue #7 — Schedule/Playout split (`schedule_entry` decoupled into a
      reusable `schedule` + `schedule_item`, plus `channel.schedule_id`):
      migration `0006_schedule_playout_split.sql` drops `schedule_entry` and
      rebuilds `epg_entry` (SQLite can't repoint a FK's target table via
      `ALTER`), auto-creating one `schedule` per channel that already had
      `schedule_entry` rows (named `"<channel name> schedule"`) and carrying
      its rows over as `schedule_item` under the same ids — no id remapping
      needed, since `epg_entry.source_schedule_item_id` (renamed from
      `source_schedule_entry_id`) keeps resolving correctly. `internal/api`:
      `schedules.go` now serves `schedule` CRUD (`/schedules`) and
      `schedule_item` CRUD (`/schedules/{id}/items`,
      `/schedule-items/{id}`); `channels.go` gained `schedule_id` on
      `channelRequest` (create/update), triggering the materializer only
      when it actually changes (not on every unrelated field edit).
      `internal/materializer`: `materializeChannel` now looks up a channel's
      `schedule_item` rows via its `schedule_id` instead of a direct
      `channel_id` column; also fixed the issue's point 2 — the dynamic-block
      loop cursor (`fillLoopingBlock`'s `state.seqCursor`) was keyed only by
      `blockID`, so two channels sharing one dynamic block via the same
      `schedule` would silently share (and race on) rotation position within
      one `Run()`, the opposite of ErsatzTV's "channels may not have the
      same content playing at the same time" guarantee the issue quoted; the
      key is now `channelID + "|" + blockID`. `internal/mcpserver`:
      `create_schedule_entry` replaced by `create_schedule` +
      `create_schedule_item`, plus a new `assign_channel_schedule` tool
      (channel had no `update_channel` MCP tool at all before this — adding
      one field's worth of narrow-purpose tool matched the existing
      single-job-per-tool pattern better than a full generic `update_channel`).
      Design decisions (surfaced to and made with the user, not assumed):
      chose the full named-`schedule`-entity model over the minimal
      "just a join table on the old `schedule_entry` shape" alternative, for
      the "assign a whole named grid to a channel at once" UX closer to
      ErsatzTV's own; left `FillerRule` scoping untouched (still `channel`/
      `block` only, no `schedule` scope) since filler divergence across
      channels sharing a `schedule` was already solved by that existing
      per-channel/per-block scoping, consistent with issue #4 (Deco layer)
      already treating filler as a separate concern from scheduling.
      Verified: `go build ./... && go vet ./... && go test ./...` clean,
      including a new `TestMigration0006_MigratesScheduleEntryData`
      (`internal/storage`) that seeds real pre-0006-shape data (two
      `schedule_entry` rows on one channel, one `epg_entry` sourced from one
      of them, a second channel with no scheduling at all) against a
      from-scratch 0001-0005 schema and applies 0006 for real, asserting the
      auto-created `schedule`'s name, both `schedule_item` ids surviving
      unchanged, the untouched channel's `schedule_id` staying `NULL`, and
      `epg_entry.source_schedule_item_id` resolving correctly after the
      table rebuild — the standard always-empty-database migration test
      never would have caught a mistake in the data-carryover logic itself.
      Also added `TestMaterializer_EndToEnd_DynamicBlockLoopCursorIsPerChannel`
      as the regression test for the cursor fix (two channels on one
      `schedule` airing one dynamic block; a window sized so only the first
      item fits proves neither channel's result depends on materialization
      order). Incidentally fixed `TestMCPToolsEndToEnd`, which asserted a
      `"create_block"` MCP tool that was never actually registered (block
      creation is REST/web-UI only) — pre-existing on unmodified `main`,
      caught while touching that test's tool list for this slice's renames,
      not introduced by it. Also exercised the real binary end to end
      (`go run ./cmd/server` against an isolated temp SQLite file): admin
      bootstrap, login, create channel/block/schedule/schedule-item, assign
      the schedule to the channel, confirm `schedule_id` round-trips on
      `GET /channels`, `GET /epg` still responds `200` — all behaved as
      designed.
- [x] Pull sync ingests filler content from a `homevideos`-type Jellyfin
      library: `internal/pullsync/jellyfin.go` adds `Video` to
      `IncludeItemTypes` (Jellyfin has no dedicated item type for a bumper/
      interstitial/promo clip in that kind of library — everything comes
      back tagged `Video`) and maps it to `domain.CatalogOther` in
      `mapJellyfinItem`. Verified end to end against a real Jellyfin library
      structured `<Network>/<Category>/<clip>` (categories: Bumper,
      Interstitial, Promo, Up Next) and a real test channel built on top of
      it: 4 `filler_pool`s (one per category, hand-curated — these clips
      carry no genre/tag metadata, so dynamic `criteria` pools don't apply),
      4 channel-scoped `filler_rule`s (`preroll`→Bumper, `midroll`→
      Interstitial, `postroll`→Promo, `tail`→Up Next), a curated
      programming `block`, a daily `schedule`, materialized `GET /epg`
      confirmed every kind (`preroll`/`midroll`/`postroll`/`tail`) resolving
      to the right pool. Known limitation this surfaced, not fixed here: a
      manual block that doesn't fill its whole occurrence-to-occurrence gap
      leaves `tail` responsible for the entire leftover window (materializer
      loops that pool until the next occurrence, per its documented "pad to
      the next schedule_item" job) — fine for a short bump, not for hours,
      and this project's "Up Next" content is specifically supposed to
      reflect the *actual* next scheduled item rather than loop a small
      generic pool; no design for that yet — folded into issue #4 (Deco) as
      a "base programming" requirement (see below), distinct from the "first
      N episodes of a series" curation gap this also surfaced.
- [x] Series slicing: a series block item takes an optional `slice` (first or
      last N, episode-number range, season list and/or range, exclude
      specials, skip first/last N, random sample N), resolved by the
      materializer on every run so it tracks the library; dynamic blocks and
      criteria items get no slice (they query `season_number`/
      `episode_number`). `POST /series/slice-preview` and MCP
      `preview_series_slice` give a live episode count; block editor "Slice"
      panel on a series item. See `docs/ARCHITECTURE.md` "Series block
      items". Not yet: choosing a slice while adding a series (it is set on
      the item after adding), a manual "re-roll" of a random sample.
- [x] Season 0 (specials) is left out of a block's selections by default; a
      block option `include_specials` (REST, web UI block editor, MCP) lets it
      in. See `docs/ARCHITECTURE.md` "Season 0 (specials) of a block". Not yet:
      the block editor's dynamic preview and the collection preview still count
      season 0.
- [x] Base schedule: a channel's optional `base_schedule_id` plays its own
      programming, with its own filler chain, in every gap the pinned
      schedule leaves, resuming at its natural position (REST, MCP, channel
      Deco tab). See `docs/ARCHITECTURE.md` "Base schedule".
- [x] Watermark: a channel and a block each carry an optional client-drawn
      image (inherit/disable/override, corner, opacity, size), resolved per
      guide entry and drawn over the player (REST, MCP, channel Deco tab,
      block editor). See `docs/ARCHITECTURE.md` "Watermark".
- [x] Deco tab: the channel page's one place for decorative behavior (base
      schedule, watermark, fallback filler, with per-kind filler rules under
      a collapsed Advanced section). A web UI composition over the existing
      resources, no new entity. See `docs/ARCHITECTURE.md` "Deco tab".
- [x] Issue #10 — overrun policy: "stop before end" replaces "delay the next
      occurrence" as the *only* behavior (no per-block/per-channel
      configuration — the user's own working example decided this: a
      schedule_item's nominal start is never delayed, full stop).
      `internal/materializer/expand.go`'s `expandChannel`: `pickFiller`
      extracted from the old `insertFiller` closure (selects a filler item
      without emitting it yet) so `postroll` can be picked *before* the
      item loop runs — its duration is reserved out of the budget
      (`itemsCutoff = nominalNextStart - postrollDuration`), and program
      items (plus any `midroll` between them) are dropped in order, starting
      from the first one that doesn't fit before that cutoff, down to zero
      items if even the first one alone doesn't fit — there's no
      client-side support for trimming a single item mid-playback, so
      dropping whole trailing items is the only lever (per the issue's own
      "Open questions"). This only engages when a real next occurrence
      exists in the current window (`hasNextOccurrence`) — the window's own
      end is just how far this `Run()` happened to compute, not a real
      commitment, so the last occurrence in a window is unconstrained
      exactly as before, and picks up naturally on the next `Run()`. A
      dynamic (looping) block was already exempt (`fillLoopingBlock` stops
      exactly at the boundary by construction). Design decisions made with
      the user (not assumed): no configurable policy at all (contradicted an
      earlier "per block" answer mid-conversation — resolved by the user's
      own worked example, which only makes sense under an unconditional
      rule); `postroll` counts toward the cutoff and always plays in full
      (items stop earlier to make room, per the user's own 17:00/90min-block
      + 5min-postroll/18:00-next-occurrence example); a UI warning for
      "this block is longer than its slot" stays backlog, superseded by the
      user's own, more concrete ask (see the issue #4 update above).
      Verified: `go build ./... && go vet ./... && go test ./...` clean.
      Rewrote `TestExpandChannel_OverlapPushesSecondOccurrenceLater` (tested
      the old, now-removed "delay" behavior) into
      `TestExpandChannel_OverrunDropsWholeItemAndRespectsNextStart`; added
      `TestExpandChannel_OverrunDropsTrailingItemsOnly` (partial drop: 2 of
      3 items fit, the 3rd doesn't) and
      `TestExpandChannel_OverrunReservesPostrollBeforeNextStart` (the exact
      17:00/90min/postroll-5min/18:00 scenario from the design conversation,
      asserting the program ends at 17:55, postroll runs 17:55-18:00, and
      the next occurrence starts at exactly 18:00). The pre-existing
      `TestMCPToolsEndToEnd` initially broke on this change (a `once`
      schedule_item near "now" with a 90-minute movie and a 1-hour preview
      window, no real next occurrence — the movie was being dropped against
      the *window's* end) — this is exactly what `hasNextOccurrence` fixes;
      confirmed the fix rather than loosening the test.

- [x] Continuous schedule items: `recurrence_type = continuous` (migration
      `0007`), one per schedule, filling whatever timed items leave and
      resuming its rotation; grid stability via a 3h freeze margin
      (`PLAYHEADTV_EPG_FREEZE_MARGIN`, `freeze_margin_ms` in `GET /epg/meta`)
      with the rotation persisted in `epg_entry.rotation_next`; per-channel
      force reset (`POST /channels/{id}/reset-epg`); web UI continuous option,
      "Convert to continuous" for schedules made of seven 00:00 weekly items on
      one dynamic block, and a "Reset grid" button. Schedule edits (item
      create/update/delete, channel schedule assignment) reset the affected
      channels automatically and frozen gaps are regenerated. Known tradeoffs: the freeze
      only applies to channels that have a continuous item; the client only
      carries a 3h fallback constant (`epgFreezeMarginFallback`), it does not
      act on the margin.

- [x] Manual blocks: `block.list_mode` `repeat` (default) or `once` (migration
      `0015`), for timed and continuous items alike; rotation of a `once`
      continuous item persisted in `rotation_next`; zero-progress guard and
      `repeat-empty` epg warning; existing blocks migrated to `repeat` with a
      review list (`GET /blocks/list-mode-review`, startup log, web UI notice)
      for the ones scheduled at fixed times; API, MCP and web UI expose the mode.

- [x] Weekly schedule items hold a list of weekdays (`days_of_week`, migration
      `0016`; existing items became a list of one day and the single-day column
      was dropped, with no alias): one row, one write and one reset for
      "Thursday and Friday at 22:00" or "every day at 18:00". REST and MCP
      accept and return the list (at least one day, no duplicates, only for
      weekly items); the materializer expands one occurrence per day; the web UI
      has a weekday multi-select with every-day/weekdays/weekend shortcuts and a
      compact label in the Programming tab, and "Convert to continuous" still
      recognizes a 24/7 rotation made of 00:00 weekly items covering each
      weekday once. Known tradeoff: items already created separately are not
      merged automatically.

- [x] Continuous schedule items and dynamic blocks under a timed item apply the
      preroll, midroll, postroll and tail rules per program (a program is aired
      as a whole preroll+program+postroll unit; tail covers the leftover before
      the next timed item; a kept head resumes mid-unit exactly), with a
      `filler-ratio` epg warning when fillers between two programs can exceed 50%
      of the shortest program and a hard cap on entries per fill. A dynamic block
      under a timed item changes shape (preroll once at the top is gone), so
      its already-materialized grid is rebuilt on the next regeneration; no data
      migration.

- [x] Explicit block tail behavior: `filler_rule.max_fit_attempts` (migration
      `0017`, 1 to 20, default 1 = give up at the first miss; REST, MCP
      `create_filler_rule` and the filler rule form) lets a tail or fallback
      fill try other pool items when one does not fit and keep going with
      what fits; skipped items do not use up a sequential rotation slot. The
      cascade items, tail, channel fallback, gap is documented in
      `docs/ARCHITECTURE.md` ("Block tail and the fill cascade"), and an
      `unfilled-slot` epg warning (shown in the channel guide) reports a slot
      that can end empty with no fallback rule. Known: a manual block's last
      timed occurrence has no next item, so its tail fills up to the window's
      end (not changed).

- [x] Break templates at program boundaries (migration `0018`): an ordered list
      of `single` and `fill` (target length) steps replaces the single item of a
      preroll/midroll/postroll rule, scoped to a channel or a block (REST
      `/break-templates`, MCP `create_break_template`/`list_break_templates`, web
      UI step editor). Existing rules are untouched. Design, precedence, limits
      and resume in `docs/ARCHITECTURE.md` ("Break templates").
- [x] Items played in segments (migration `0020`): `epg_entry.item_offset_ms`
      and `split_group`, the `CutPlanner` seam (default: never cuts) and the
      materializer's splitting with a midroll break between segments, client
      playback of a segment (seek to the offset, transition at the segment end,
      second controller prepared at the resume point while the break plays),
      one merged guide program and OSD subject per split item. Design in
      `docs/ARCHITECTURE.md` ("Items played in segments").

- [x] Cut-point time model (migration `0019`): pure `internal/cutplan` (ordered
      rules chosen by kind, duration range and has-chapters; interval,
      percentage, explicit offsets, phased cadence and chapter-role strategies;
      guard rails, deterministic output, fallback chain) and stored
      `cut_rule_set` scoped to a channel or block (REST `/cut-rule-sets` with a
      preview endpoint, MCP `create_cut_rule_set`/`list_cut_rule_sets`/
      `preview_cut_plan`, web UI Cut rules with a live preview). Semantics in
      `docs/ARCHITECTURE.md` ("Cut points").
      The materializer cuts items by the stored sets (`RuleSetCuts` behind the
      `CutPlanner` seam, chapters read from `catalog_item.chapters`; a rule set
      write regenerates the guide). The `after_intro` role prefers the end of
      the item's Jellyfin Intro media segment (migration `0021`, read by the
      pull sync from Jellyfin 10.10+, bounded lookups) over the chapter
      heuristic; the plan's source (segment, chapter, fallback) shows in the
      preview and the web UI.

## Phase 2 — MCP surface

- [x] MCP server (`github.com/modelcontextprotocol/go-sdk` v1.8.0) in the same
      binary, exposing a deliberately partial slice of the REST API above as
      MCP tools — new package `backend/internal/mcpserver` (one file per
      resource, same layout convention as `internal/api`), wired from
      `cmd/server/main.go` right after `api.Register(...)`, sharing the exact
      same `db`/`mat`/`encKey` the REST handlers use so a write through a tool
      is immediately visible to the REST API/web UI and vice versa. The
      installed SDK's real API (discovered via `go doc` + the module's own
      `examples/http`, not assumed from memory) is: `mcp.NewServer` +
      `mcp.AddTool[In, Out]` (generic, struct-tag-driven JSON Schema
      inference via `github.com/google/jsonschema-go` — a field needs
      `json:"...,omitempty"` to become an optional schema property, not just
      a pointer type) for each tool, and `mcp.NewStreamableHTTPHandler(getServer,
      opts)` (an `http.Handler`) for the transport, mounted at `POST/GET /mcp`
      via a plain `mux.Handle("/mcp", handler)` — no different from any other
      route on the mux. Auth needed zero changes to
      `internal/api/auth.go`: `/mcp` isn't added to `isOpenPath` or
      `deviceOrSessionGET`, so it falls into `RequireAuth`'s default
      "everything else requires a session" branch by exclusion, exactly like
      `/blocks*`/`/filler-*`/`/schedule-entries*` already do — callers send
      the same `Authorization: Bearer <session token>` `POST /sessions`
      already returns, on every MCP HTTP request. Like
      `internal/materializer`, this package does not import `internal/api`:
      it duplicates the handful of constants it needs (`DefaultTenantID`,
      column-alias `SELECT` strings) instead, so it stays decoupled from
      `internal/api`'s unexported identifiers and keeps working unchanged
      once real multi-tenancy exists.

      The 8 tools (exactly the parenthetical list this bullet used to read,
      no more): `create_channel`/`list_channels` (channel), `register_media_source`
      (probes real Jellyfin connectivity — `GET <base_url>/System/Info/Public`,
      5s timeout — to decide `sync_mode` pull/push, same as the REST handler;
      encrypts an optional `credential` with the same `encKey`), `search_catalog`
      (title/series-title substring search, case- and accent-insensitive via `catalog_item.search_text`, default/max limits 50/200, same as REST),
      `create_block` + `add_block_item` (position always `MAX(position)+1`;
      `update_block_item` reorders/trims, mirroring `PUT /blocks/{id}/items/{item_id}`)
      + `list_block_items` (added beyond the literal REST 1:1 mapping so an
      agent can verify its own `build_block` result without leaving MCP),
      `create_filler_pool` + `add_filler_pool_item` + `create_filler_rule` +
      `list_filler_pools` (the minimal useful `manage_filler` surface — no
      update/delete), and `create_schedule_entry` (replicates
      `validateScheduleEntryRequest`'s weekly/once mutual-exclusion rules,
      stricter than today's REST validator: weekly forbids `start_at` and
      once forbids `day_of_week`/`start_time_local`, not just requiring the
      right fields) + `preview_epg` (reads `epg_entry` for a window, one
      channel or all). Every write that triggers materialization on the REST
      side (`add_block_item`, `add_filler_pool_item`, `create_filler_rule`,
      `create_schedule_entry`) calls the same `mat.Trigger()` here.
      Deliberately out of scope for this v1 slice, matching the roadmap
      bullet's own parenthetical rather than the full REST surface: full CRUD
      (update/delete) on `media_source`/`filler_pool`/`filler_rule`,
      `catalog-sync` (a device→backend push flow, not authoring),
      devices/pairing, and any auth/session-issuing tool (a caller must
      already hold a valid session obtained the normal way; MCP never expands
      the auth surface).

      Verified live, not just reviewed: `go build ./... && go vet ./...`
      clean; an automated test
      (`backend/internal/mcpserver/mcpserver_test.go`) spins up a real
      `httptest.Server` running the exact same `RequireAuth(api.Register +
      mcpserver.Register)` stack `cmd/server/main.go` builds, bootstraps a
      real app_user via `POST /app-users` + `POST /sessions` (open
      endpoints), then drives it with a real `mcp.Client` over the real
      streamable-HTTP transport (`mcp.StreamableClientTransport`, actual
      JSON-RPC over HTTP, not the handler functions called directly) — this
      is the actual MCP protocol working end to end, not just the SQL. It
      confirms `/mcp` rejects a client with no bearer token at all
      (`Connect` fails), lists all 13 registered tool functions (8 tools plus
      the extra `list_block_items`/`list_filler_pools` verification helpers)
      each with a non-empty description, then walks the full flow through
      MCP alone wherever the roadmap scope allows it: create a channel,
      register a `local_folder` media source (push `sync_mode`) and a
      `jellyfin` one against a fake Jellyfin `httptest.Server` that really
      answers `GET /System/Info/Public` with 200 (confirms the real probe
      path, not a stub, decides `sync_mode: pull`), push synthetic catalog
      items via the (intentionally non-MCP) REST catalog-sync endpoint,
      `search_catalog` to resolve their IDs, build a block and add an item to
      it, build a filler pool/item/rule, reject an invalid (weekly +
      `start_at` set) `create_schedule_entry` call with the expected error
      text, create a real "once" schedule entry ~2s in the past, and poll
      `preview_epg` for up to 5s until the real, running materializer (not
      mocked) produces both the scheduled program entry and its `fallback`
      filler entry — confirming the entire chain (tool → SQL write →
      `mat.Trigger()` → background materialization → readable via another
      tool) actually works, not just that each handler compiles. Separately,
      also verified against a real, isolated `go run ./cmd/server` instance
      (`PLAYHEADTV_DB_PATH` in a temp dir, a free port via
      `PLAYHEADTV_ADDR`, separate from any `docker compose` instance) using a
      throwaway manual Go MCP client (not part of the deliverable) connecting
      over real network sockets to that instance's `/mcp` endpoint with a
      real session Bearer token: every one of the 8 tools plus the 2
      verification-helper tools called successfully, ending with the same
      materialized program+fallback result observed in the automated test.
      `go test ./...` passes in full.

- [x] MCP covers everything the web UI covers: one tool per REST operation
      (channel update/delete/warnings/reset, block, schedule and schedule
      item, filler pool/rule, break template and cut rule set read/update/
      delete, media source CRUD plus catalog and pull sync, devices, stream
      preferences, EPG read), as thin in-process REST dispatch so validation
      and the materializer trigger are the REST handlers'. Destructive tools
      need `confirm=true`; credentials and device tokens are never returned by
      a read tool; `TestEveryRESTRouteHasAnMCPTool` fails on a REST route with
      no tool and no justified exception. See `docs/ARCHITECTURE.md` "MCP
      surface". No permission model exists beyond a valid session, so the new
      tools run under the session the MCP caller already has.

## Phase 3 — Web UI (authoring)

- [x] Web UI scaffolding + auth + channel authoring: React + TypeScript +
      Vite, shadcn/ui (Radix + Tailwind), TanStack Query, Zustand — built as
      a static SPA (`webui/`) and served by the Go binary itself under
      `/admin/` (embedded via `backend/internal/webui`, no separate Node
      server in production, same-origin so no CORS needed). Login
      (`POST /sessions`) and full channel CRUD (list/create/edit/delete)
      verified end to end (Playwright + a real Docker build).
- [x] EPG viewer as the admin home + sidebar nav: `webui/src/pages/EpgPage.tsx`
      is now the `/` route (`webui/src/App.tsx`), rendered as a classic
      multi-channel TV guide grid (Plex/Emby "Live TV Guide" layout, kept in
      the existing light shadcn design system, not the reference screenshot's
      dark palette) instead of the original single-channel vertical list.
      Every channel is a row (fetched once via `GET /channels`); a single
      `GET /epg?start=&end=` call (no `channel_id`) fetches all channels'
      entries for a `now-30min .. now+8h` window, grouped client-side by
      `channel_id`. Time is the horizontal axis: a sticky top ruler
      (`position: sticky; top: 0`) marks every 30 minutes, and each program
      block is absolutely positioned/sized from `PIXELS_PER_MINUTE` so
      duration maps to width; long titles truncate
      (`overflow-hidden`/`text-ellipsis`) instead of breaking the row height.
      The channel-number/name column is `position: sticky; left: 0`, so
      scrolling the grid horizontally keeps both the channel column and the
      time ruler frozen in place ("frozen row + frozen column" spreadsheet
      behavior) — this was the central ask and was explicitly tested (see
      below). On load, the grid auto-scrolls so "now" sits near the left
      edge with ~20min of past context, and a vertical "now" line
      (`bg-destructive`) crosses all rows at the current-time x position; the
      currently-airing block per channel is still highlighted, reusing the
      existing `start_at <= now < end_at` "Now" logic. A channel with no
      entries in the window shows a sticky "No programming" label instead of
      an empty row. Titles still resolve via `GET /catalog-items/{id}`
      (cached per id with TanStack Query) — no backend change. Verified
      end-to-end: `npx tsc --noEmit` + `npm run build` clean, a real
      `docker compose build backend && docker compose up -d` + `/healthz`
      check, and Playwright against that running container — login lands on
      `/` showing the grid with both seeded channels as rows, proportional
      block widths, and the "now" line inside the initially-scrolled view;
      the horizontal scroll was driven programmatically
      (`scrollLeft +=`/`scrollLeft = 0`) and screenshots confirmed the
      channel column and ruler stayed fixed while the program blocks scrolled
      underneath; zero console errors/warnings throughout. (Along the way,
      fixed a real bug caught by this same Playwright pass: the initial
      auto-scroll-to-"now" effect depended only on the time window, so on a
      fresh load it fired before the grid — gated on channels having
      loaded — had mounted, leaving `scrollLeft` stuck at 0; it now also
      depends on the grid being mounted.)
- [x] Extend the web UI to block/schedule authoring — the rest of "minimal
      web UI for channel/block/schedule authoring", the primary authoring
      surface (see `ARCHITECTURE.md`, "Authoring happens in the web UI + MCP,
      not in playback clients"). Two new admin pages, added to the existing
      React/TS/Vite/shadcn/TanStack Query app with no backend changes (the
      `block`/`block_item`/`schedule_entry` REST surface already existed):
      `webui/src/pages/BlocksPage.tsx` (list + create/edit/delete, same
      table/dialog pattern as `ChannelsPage.tsx`) and
      `webui/src/pages/SchedulePage.tsx` (list + create/edit/delete). A
      block's items are managed from a detail dialog
      (`webui/src/components/BlockItemsDialog.tsx`, opened via an "Items"
      button per row rather than a `/blocks/:id` route) showing the
      position-ordered list (titles resolved via `GET /catalog-items/{id}`,
      cached with TanStack Query `staleTime: Infinity`, same pattern as
      `EpgPage.tsx`), a debounced (300ms) `GET /catalog-items?q=` search to
      add an item to the end, and a remove button per item — the UI does not
      reorder yet (`PUT /blocks/{id}/items/{item_id}` exists in the API, not
      wired into this dialog). The schedule entry
      form (`webui/src/components/ScheduleEntryDialog.tsx`) mirrors
      `validateScheduleEntryRequest` from `backend/internal/api/schedules.go`
      client-side: a weekly/once toggle swaps between a day-of-week +
      "HH:MM" pair and a single `datetime-local` field, so the two mutually
      exclusive shapes never both hit the API, and the always-invalid
      combinations are caught before the request goes out. Two shadcn
      components didn't exist yet and were added via `npx shadcn@latest add`
      (`select`, for the channel/block/day pickers, and `switch`, for the
      `active` toggle) instead of hand-rolling either. `Layout.tsx` gained
      "Blocks" (`Layers` icon) and "Schedule" (`CalendarDays` icon) nav
      entries; `App.tsx` registered `/blocks` and `/schedule` alongside
      `/channels` under the existing `RequireAuth`+`Layout` route. Verified
      end to end: `npx tsc --noEmit` + `npm run build` clean; a real,
      isolated `go run ./cmd/server` instance (own temp SQLite file and
      port, separate from any `docker compose` instance)
      with a freshly created admin user, one channel, a `local_folder` media
      source, and three synthetic catalog items pushed via
      `POST /media-sources/{id}/catalog-sync`; then, via Playwright against
      that instance: logged in, created a block, added all three catalog
      items (confirmed positions 0/1/2 and resolved titles/durations),
      removed the middle one (confirmed positions stayed 0/2, not
      renumbered — expected, matches the no-reorder limitation above),
      created one weekly schedule entry (edited it — toggled `active` off
      then back on, confirmed the table's Active/Inactive label followed —
      then deleted it) and one "once" entry a few minutes in the future
      (left in place). Confirmed the materializer actually ran: polling
      `GET /epg` a few seconds later showed two real `epg_entry` rows
      (`source_schedule_entry_id` pointing at the created entry) for the
      surviving two block items back-to-back at the exact scheduled
      instant. Zero browser console errors/warnings throughout. Killed the
      isolated backend afterward; `go vet ./...` still clean.
- [x] Media source registration flow: the last Phase 3 item, closing the
      phase. Two new admin pages added with no backend changes at all (both
      the `media_source` probe/CRUD surface and `device` pairing surface
      already existed): `webui/src/pages/MediaSourcesPage.tsx` (list +
      create/edit/delete, same table/dialog pattern as the other authoring
      pages) and `webui/src/pages/DevicesPage.tsx` (list + delete). The
      create/edit form (`webui/src/components/MediaSourceDialog.tsx`) has a
      type select (`jellyfin`/`plex`/`local_folder`), name, a base-URL field
      that only renders for non-`local_folder` types, and an optional
      credential field that is a) never pre-filled when editing — the API
      never returns it back, `CredentialCipher` has `json:"-"` — and b) only
      included in the request body when non-empty, since the backend's
      `Credential *string` field only stays `nil` (leaving a previously-saved
      credential untouched) when the JSON key is absent entirely; an empty
      string would still be a non-nil pointer and overwrite it. Each row
      shows the probe result: `sync_mode == "pull"` (only reachable by a
      successful `jellyfin` probe) gets a plain positive badge ("Connected
      directly"), nothing else needed since the backend consumes that
      catalog on its own outside this screen's scope; `sync_mode == "push"`
      gets a neutral badge ("Requires sync via app") plus a
      per-row expandable panel (a second `TableRow`, no new shadcn
      dependency) with pairing instructions and a "Create device" button
      — the panel's explanation text distinguishes a `jellyfin` source whose
      probe actually failed (`last_probe_ok === false`, "could not connect
      directly… the connection failed or timed out") from a
      `plex`/`local_folder` source that never attempts a direct connection at
      all, so a real connectivity failure never gets described the same way
      as an by-design push-only type. Device pairing
      (`webui/src/components/CreateDeviceDialog.tsx`) is one dialog shared
      between both pages (opened from a media source's pairing panel, or
      standalone from `DevicesPage.tsx`'s own "Create device" button):
      submitting name+platform calls `POST /devices` and swaps the same
      dialog into a token-reveal step, since that response is the only time
      the raw token is ever returned — a destructive-styled warning banner
      ("copy it now, the API will never return it again") sits above a
      read-only, click-to-select input plus a `navigator.clipboard` "Copy"
      button. `webui/src/lib/types.ts` gained `MediaSource`, `MediaSourceInput`,
      `SourceType`, `SyncMode`, `Device`, `DeviceInput`, `Platform`, and
      `CreateDeviceResponse`, mirroring `backend/internal/domain.MediaSource`/
      `Device` and the request/response shapes in
      `backend/internal/api/media_sources.go`/`auth.go`. `Layout.tsx` gained
      "Media sources" (`Server` icon) and "Devices" (`Smartphone`
      icon); `App.tsx` registered `/media-sources` and `/devices`. Verified
      end to end: `npx tsc --noEmit` + `npm run build` clean. Live
      verification ran against an isolated backend instance (own temp SQLite
      file and port) with a freshly built `webui` dist (embedded via
      `go:embed`). Via Playwright
      against that instance: created a `local_folder` source (no
      `base_url`) — confirmed it shows push with the type-appropriate
      "does not connect directly" wording, not a false connection-failure
      claim; created a `jellyfin` source pointing at `http://127.0.0.1:1`
      (a real closed port, so the probe's 5s-timeout HTTP client hit a real
      connection-refused, not a simulated failure) — confirmed it also
      lands as push, this time with the probe-failure-specific wording;
      created a `jellyfin` source pointing at a `python3 -m http.server`
      serving a static file at exactly `/System/Info/Public` (a real HTTP
      200, not an actual Jellyfin response — the probe only checks the
      status code, so this is a faithful exercise of the `sync_mode: pull`
      branch without needing a real Jellyfin server) — confirmed it lands
      as pull with no pairing panel offered. From the failed-probe source's
      pairing panel, created a device (name + Android TV), confirmed the
      token dialog appeared with the warning banner, and confirmed the
      "Copy" button actually wrote to the clipboard (button label flipped
      to "Copied", toast confirmed). Went to Devices, confirmed the
      device appeared (no token column) with "never" for last-seen, deleted
      it, confirmed it disappeared. Edited the `local_folder` source's name,
      confirmed the new name persisted after a full reload; deleted it,
      confirmed it left the list. Deleted the pull-mode test source too.
      Zero browser console errors/warnings across the entire pass.
      `go build ./...` and `go vet ./...` both pass clean — nothing here
      touched `backend/` at all.

## Phase 4 — Client wiring

- [x] Replaced the spike's hardcoded 3-item test channel with real
      `GET /channels` + `GET /epg` + `GET /catalog-items/{id}` resolution
      (`client/lib/playhead_api.dart`, wired into `client/lib/main.dart`).
      New dart-defines: `PLAYHEADTV_BASE_URL` (required), `PLAYHEADTV_DEVICE_TOKEN`
      and `PLAYHEADTV_CHANNEL_ID` (optional — channel auto-detects by lowest
      `number` when unset). Fetches a single 6h EPG window at startup (no
      rolling/polling re-fetch yet — out of scope for this slice). Joining a
      channel already mid-program seeks the first item to the correct offset
      instead of restarting it, and schedules that item's own crossfade off
      the *remaining* duration, not the full one. Up/down arrow keys switch
      channels (`KeyboardListener`), cycling by `number` and re-resolving the
      target channel's grid from scratch; a `_loadGeneration` counter aborts
      any preload/crossfade/grid-load still in flight from a channel that's
      no longer current, so rapid switching can't apply a stale channel's
      result on top of the new one. Verified against a real Jellyfin server
      (manual run + screenshots, not an automated test): mid-program join
      seek, remaining-duration crossfade scheduling, and channel switching
      all confirmed working end to end.
- [ ] Push-sync relay: local checkpoint tracking (last `DateModified` seen per
      source) + `POST /media-sources/{id}/catalog-sync` on foreground/manual
      trigger. Only exercised once a `push`-mode source exists (cloud-sync
      tier, or a self-hosted backend that can't reach its own Jellyfin).

## Phase 5 — Playback client UX

- [x] Real on-screen channel guide: `client/lib/main.dart` now shows a proper
      OSD (channel number/name badge, current program with a play icon, a
      progress bar read from the *live* `VideoPlayerController` position/
      duration — not the scheduled clip length, so it never desyncs from
      buffering/seeking — and an italicized "Up next" line, omitted when
      there's no next item). Triggers on every channel start and crossfade
      completion, auto-hides after ~5s via `AnimatedOpacity`. The old debug
      overlay (`channelLabel` + raw `_status`) is kept, but only as the
      fallback for the no-content-yet states (bootstrap, mid-switch, or an
      unresolved EPG error) that the OSD itself can't represent. Verified
      against a real Jellyfin server with live schedule data: OSD content,
      the live progress bar, "up next", the ~5s auto-fade, and reappearance
      on a fresh channel load were all confirmed via manual run + screenshots
      (not an automated test).
- [x] Remote-control-style navigation beyond up/down: `client/lib/main.dart`
      adds two keys on top of the existing arrow-key channel switch — `I`
      ("Info") toggles the OSD on demand, and `0`-`9` accumulate a
      channel-number jump. Getting there required refactoring
      `_switchChannel(delta)`: it now only resolves the relative target index
      and hands off to a new `_switchToChannelIndex(targetIndex)`, which holds
      the actual "cancel everything about the current channel, then load the
      target" core — both the arrow-key path and the new number-jump path
      call it, so a successful switch by either input always cancels every
      timer (including the new digit-commit timer) and clears the digit
      buffer, exactly like the existing timer cancellation already did for
      arrows alone. `_toggleInfo()`: if `_osdVisible` is already true, hides
      immediately (cancels `_osdHideTimer` and, for the same
      no-wasted-cycles reason `_showOsd`'s own auto-hide branch already
      stops it, `_osdTickTimer` too) instead of waiting out the ~5s
      auto-hide; if hidden, just calls the existing `_showOsd()` unchanged,
      so auto-show/auto-hide behavior is untouched. Digit entry
      (`_onDigitKey`/`_commitChannelInput`): each `0`-`9` keydown appends to
      `_channelInputBuffer` (a new small "Channel: NN_" overlay, top-right,
      shows whenever it's non-empty — deliberately not gated on `hasContent`,
      so it works even before/between channel loads) and (re)arms a 1.5s
      `_channelInputCommitTimer`; hitting `channelInputMaxDigits` (3) commits
      immediately instead of waiting. Commit resolves the buffered digits
      against `_channels` by exact `number` match: found (and not already the
      current channel) switches via `_switchToChannelIndex`; not found clears
      the buffer and sets `_status` to `"ERROR: channel N not found"`,
      reusing the exact existing `_status`-starts-with-"ERROR" banner
      convention (rendered as the red banner when `hasContent`, or folded
      into the pre-existing debug overlay text otherwise) — no new UI pattern
      invented, and no crash/hang either way.
      Verified: `fvm flutter analyze lib/main.dart` clean ("No issues
      found!"). `fvm flutter run -d linux` against a running backend and a
      real Jellyfin: bootstrap loaded the 2 test channels (`GET /channels`
      returned channels #1 and #2) and the app ran with no exceptions in the
      log.
      **Not observed live, code review only**: the actual keyboard
      interactions (Info toggle hiding/reshowing the OSD, digits accumulating
      and rendering the channel-number overlay, the 1.5s auto-commit and the
      3-digit immediate-commit, the "channel not found" error banner, and an
      arrow-key switch clearing an in-progress digit buffer) were not
      exercised end to end with real key events in this slice. The test
      channels' `epg_entry` data had also fully run dry (`GET /epg` returned
      only past-dated entries regardless of window size), which would have
      blocked the OSD-dependent Info-toggle check anyway, since the OSD only
      ever appears during active playback (`_startChannel`/`_crossfadeTo`/
      `_resumeAt`). Extending it needs a `POST /schedule-entries` (or
      updating an existing one), which is session-auth-only
      (`internal/api/auth.go`'s `RequireAuth`); it was left as a documented
      gap rather than worked around (e.g. no direct SQLite write against a
      running instance). Redo the live pass end to end with fresh EPG data
      before trusting this beyond the code review above.
- [x] Guide hides filler: the full-screen EPG, the web UI EPG page and the
      channel Guide tab show only `program` and `fallback` entries; the time
      of hidden preroll/midroll/postroll/tail entries is attributed to a
      neighbouring program so the grid has no false holes (rules in
      `docs/ARCHITECTURE.md`, "Guide display"). The OSD "Up next" line skips
      filler. The web UI has a local "Show filler" toggle for debugging.
      Always on: a per-channel or per-block setting belongs to the Deco
      discussion.
- [x] Rolling EPG window: `client/lib/main.dart` now keeps pulling ahead of
      "now" instead of fetching one fixed window and stopping. A new cursor
      field (`_epgLoadedUntil`, the `endAt` of the last `EpgEntry` folded
      into `channel` for the current load generation, reset on every new
      `_loadAndPlayChannel`) tracks where the next fetch should continue
      from. After every item transition that becomes the new "current" one
      (end of `_startChannel`, end of the `step >= steps` crossfade branch,
      and end of the new `_resumeAt`), `_maybeExtendGrid` sums the
      `clipDuration` of everything from that index onward and, if that
      "runway" drops under `epgExtendThreshold` (2h), fires `GET /epg` from
      `_epgLoadedUntil` to `_epgLoadedUntil + epgWindow` in the background
      (every caller — the proactive trigger, `_crossfadeTo`'s immediate
      dead-end attempt, and the gap-retry loop below — coalesces onto a
      single in-flight fetch via `_pendingExtend`, so two callers racing
      close together can't both append the same fetched slice twice),
      defensively drops any returned entry starting before the cursor, and
      appends the rest to `channel` — never replacing the list or touching indices
      already in use by scheduled timers/controllers. `_crossfadeTo`'s old
      dead end (`nextIndex >= channel.length` → permanent "end of loaded
      grid" status) now tries one immediate fetch first and, if that lands
      new items, resumes via `_resumeAt` (a hard cut, since nothing was
      preloaded for the just-arrived item); if the grid is genuinely empty
      (a real gap, not just an exhausted window), it shows an
      "ERROR: nothing else scheduled right now — retrying…" status and
      arms a periodic `_gapRetryTimer` (`epgGapRetryInterval`, 20s) that
      keeps retrying until something materializes and resumes playback on
      its own — no manual channel switch needed. `_gapRetryTimer` joins the
      other timers cancelled in `_switchChannel`/`dispose`.
      Verified with a real run (`fvm flutter run -d linux` against the real
      backend + Jellyfin, `epgWindow`/`epgExtendThreshold` temporarily
      shrunk to 25min/18min to observe it in minutes instead of hours, then
      reverted before commit): the proactive threshold trigger fired
      exactly as predicted from the test channel's schedule (log evidence:
      `remaining=0:10:00` crossing the 18min threshold, fetch appending 3
      new items, cursor advancing 30min, `channel` growing 3→6), playback
      kept going with real, distinct content well past what the original
      small window would have covered, and switching channels away and
      back (keyboard input) left exactly one audio stream and no leaked timers
      or crashes across several switches. The reactive dead-end fetch and
      the `_gapRetryTimer` real-gap-recovery path were exercised only by
      code review, not observed live — the ~2h30 of contiguous test data
      materialized for this run never actually ran out during the
      observation window, so the proactive path always won before either
      fallback could trigger. Caught in review (not by the live run, which
      never hit the timing window to trigger it): the first version guarded
      only the proactive trigger against overlapping fetches, so a slow
      proactive fetch still in flight when `_crossfadeTo` hit the dead end
      could race a second, unguarded fetch of the same cursor and duplicate
      content in `channel` — fixed by coalescing every caller onto one
      shared in-flight fetch (`_pendingExtend`, described above).
- [x] Custom stream (audio/subtitle) selector: the 4-tier cascade from the
      competitive-analysis synthesis (`docs/competitive-analysis/ersatztv.md`
      section 6/7) — tier 1 (item/channel local override) stays client-local
      and unbuilt (no UI designed yet, explicitly out of scope), tiers 2-4
      (user profile, channel admin, global admin) are backed by the
      backend and resolved client-side, most-specific tier first.
      Backend: `domain.UserStreamPreference` (one row per `app_user`) and
      `domain.StreamPreferenceRule` (`scope_type` global|channel, reusing
      `FillerRule`'s shape with its own enum — `backend/internal/domain/
      stream_preference.go`), migration `0005_stream_preference.sql`,
      `GET`/`PUT /stream-preferences/me` (session-scoped to the caller),
      `GET`/`PUT /stream-preferences/config` (scope_type+scope_id), and
      `GET /stream-preferences/meta` (an updated_at-derived revision —
      deliberately not a counter table like `epg_revision`, since that
      exists for full-window regeneration, which doesn't apply to plain
      CRUD config rows) — `backend/internal/api/stream_preferences.go`.
      Client: `StreamPreferencesService` caches tiers 2+4 in
      `shared_preferences` (fetched at boot, polled via `/meta` every 5
      minutes) and tier 3 lazily per channel;
      `StreamSelectionResolver` is a pure function matching the cascade
      against the real track list from `getMediaInfo()`; `_createController`
      (`client/lib/main.dart`) applies the result via
      `setAudioTracks`/`setSubtitleTracks`, replacing the old unconditional
      `setSubtitleTracks([])`. Known gap: the three new endpoints are
      session-authenticated (same tier as other authoring endpoints), but
      the playback client currently only holds a device token (no login
      flow exists) — a device-token-only deployment gets a 401 on every
      fetch, which the caching service treats as "no preference configured"
      rather than a hard failure, so playback is unaffected but the feature
      is inert until the client is configured with a session token instead
      (or a future client login flow exists). Not resolved in this slice —
      flagged rather than worked around, since changing the endpoints'
      auth tier would contradict the issue's own explicit design.
      Verified: `cd backend && go build ./... && go vet ./... && go test
      ./...` clean; a real, isolated `go run ./cmd/server` instance
      (own temp SQLite file and port) exercised end to end via `curl`
      (bootstrap, login, GET/PUT tier 2, GET/PUT tier 3 and 4, upsert
      idempotency, validation errors, meta revision bump) — all behaved as
      designed. Client: `fvm flutter analyze lib/` clean; a real
      `fvm flutter test integration_test/channel_navigation_test.dart -d
      linux` pass (own isolated backend + fixture video server, device
      token only, confirming the graceful-degradation path above) plus
      `mosaic_test.dart` and `epg_overlay_test.dart`, all green. One case
      in `channel_navigation_test.dart` ("typing an existing channel number
      switches channel") failed intermittently — reproduced against a clean
      stash of this same commit too (2 of 3 baseline runs), confirming the
      pre-existing flakiness already documented in `AGENTS.md`'s gotchas,
      not a regression from this slice. No dedicated unit test coverage was
      added for `StreamSelectionResolver` itself (pure-function logic would
      benefit from one — left as a follow-up, not silently skipped).
      Admin UI: `webui/src/pages/StreamSelectorPage.tsx` +
      `webui/src/components/StreamCascadeDialog.tsx` fill the gap noted
      above — tiers 3-4 (channel/global) can now be viewed and edited from
      the web UI instead of raw `curl`, following the `FillerRulesPage`/
      `FillerRuleDialog` precedent. The page shows the always-present global
      cascade plus a list of configured per-channel overrides (fetched
      per-channel via `GET /channels` + one `/stream-preferences/config`
      call each, mirroring `FillerPoolItemsDialog`'s per-id `useQueries`
      pattern), with a channel picker to add a new override; the dialog
      edits the ordered step list (add/remove/reorder, audio/subtitle
      language, title block/allowlist) and `PUT`s to
      `/stream-preferences/config?scope_type=...&scope_id=...` (query
      params, not a body field, per that endpoint's actual contract).
      "Clear" on a channel row `PUT`s an empty cascade rather than deleting
      the row — there's no DELETE endpoint, and an empty cascade already
      means "no preference" to the resolver. Tier 2 (a user's own
      preference) is still out of scope — no logged-in-user concept exists
      in the client yet. Gotcha hit and fixed: `encoding/json` marshals a
      nil `[]StreamRuleStep` as `null`, not `[]` — an unconfigured scope's
      `GET` response is `{"rules":{"steps":null}}`, which crashed the UI
      until every read site treated `null` the same as an empty list (see
      `StreamRuleCascade`'s doc comment in `webui/src/lib/types.ts`).
      Verified: `tsc --noEmit`/`npm run build` clean; a real Playwright pass
      against an isolated `go run ./cmd/server` instance (own temp SQLite
      file, port 19394, admin bootstrap via
      `PLAYHEADTV_SEED_ADMIN_EMAIL`/`_PASSWORD`) — logged in, set a 2-step
      global cascade (including the null-subtitle-language and
      wildcard-language cases), reordered steps, added a channel override,
      confirmed it round-tripped through `curl` against the same instance,
      edited it back open to confirm the pre-fill matched what was saved,
      cleared it, and confirmed both the page and a direct `curl` reflected
      the empty cascade after a full page reload.
      Tier 1 (client-local item/channel override, the one piece explicitly
      left unbuilt above) is now implemented, following the "6-button
      baseline" principle added to `AGENTS.md`: a required, 6-button-only
      on-screen track picker overlay (`InputAction.trackSelector`, default
      `arrowRight`, opened from the base player state with the OSD visible —
      `client/lib/widgets/track_selector_overlay.dart`, wired into
      `_ChannelPlayerPageState` in `client/lib/main.dart` the same
      `ValueNotifier<bool>` + `ValueListenableBuilder` way as the EPG
      overlay) lists the real, current Audio/Subtitle tracks from
      `getMediaInfo()`, applies the focused choice live via
      `setAudioTracks`/`setSubtitleTracks` on the controller already
      playing, and persists it in `shared_preferences` keyed by channel id
      (`SettingsService.get/setAudioTrackOverride`,
      `get/setSubtitleTrackOverride` — client-local only, never sent to the
      backend, per the issue's original design). Two optional power-user
      accelerators (`InputAction.cycleAudioTrack`/`cycleSubtitleTrack`, no
      default binding — the first `InputAction`s to have none, which made
      `KeyBindingsService.effectiveBindings`/`resolve` need to treat "no
      binding at all" as a supported state instead of asserting a default
      always exists) cycle to the next track and converge on the exact same
      apply/persist/toast path as the picker, per the accelerator exception
      in `AGENTS.md` — both listed in `controls_mapping_page.dart` under a
      new "Shortcuts (optional)" group. `StreamSelectionResolver.resolve`
      gained a `TierOneOverride` parameter, tried first and per-axis (an
      audio-only override doesn't force subtitles to fall back to "none" —
      the untouched axis still falls through to tiers 2-4), replacing the
      `TODO(tier 1)` that used to start the cascade at tier 2.
      Verified: `fvm flutter analyze` clean (lib + integration_test);
      `go build`/`go vet`/`go test` clean in `backend/` (no backend files
      touched by this slice, run anyway per the checklist). The e2e coverage
      (opening the picker with `arrowRight` while the OSD is visible;
      `arrowUp`/`arrowDown` moving focus within the panel without switching
      channel; `Back` closing without persisting; `Select` applying live and
      persisting with a toast; revisiting the channel later showing the
      persisted choice pre-focused; a real channel switch right after a live
      `Select` not stalling) passes reliably, but split across two files —
      `track_selector_test.dart` (3 tests) and
      `track_selector_persistence_test.dart` (2 tests) — each re-run twice
      clean. That split is load-bearing, not organizational: several real
      `PlayheadSpikeApp` boots (real dual crossfade `VideoPlayerController`s,
      real GL textures) in ONE `flutter test` process intermittently stall a
      later boot's rendering pipeline indefinitely — reproduced at a
      different boot count each time (3rd, 4th, 5th), and confirmed during
      this slice's own validation to also happen on a clean, unmodified
      stash of `main` under the same loaded machine, so it's a pre-existing
      `flutter_tester`/mdk resource-pressure gotcha (see `AGENTS.md`'s
      GL-texture-under-load entry), not something this slice introduced.
      Confirmed separately, as a real single-session e2e check (not just
      code review), that the production scenario this could plausibly affect
      — a live `setAudioTracks` call followed by a real channel switch in
      the SAME app session — works correctly and fast; only *separate app
      boots in one test process* are at risk. `video_fixture.dart`'s 2nd
      audio track (needed for the picker's real navigation tests) was made
      opt-in (`start({int audioTracks = 1})`) rather than the suite's new
      default, after it briefly regressed `controls_mapping_test.dart`'s
      reliability too; that file's own new "accelerators are listed unbound"
      test also had 2 real bugs fixed (an unscoped `find.byType(ListView)`
      matching both the pushed page and the Settings page still in the tree,
      and a lazy-sliver visibility gap between two list items), unrelated to
      the boot-stacking issue. `channel_navigation_test.dart`,
      `epg_overlay_test.dart`, and `mosaic_test.dart` re-run clean (no
      regression); the pre-existing intermittent flake in
      `channel_navigation_test.dart`'s "typing an existing channel number
      switches channel" (documented in `AGENTS.md`'s gotchas) recurred during
      this validation, including once against unmodified `main` — confirmed
      pre-existing, not chased.

- [x] Fixed a real segfault reported from a release build
      (`./build/linux/x64/release/bundle/client`), reproduced by switching
      channels in quick succession: `fvp`/libmdk creates GL resources
      (fbo/tex) on its own raster thread during `initialize()` and frees them
      on a separate dispose thread during `dispose()`, with no
      synchronization between the two — and those threads are process-global,
      shared by every `VideoPlayerController` instance in the app (the
      channel player's crossfade pair AND the mosaic page's own, separate
      preview controller), not one pair per controller. A fast channel switch
      left a stale controller's fire-and-forget `dispose()` (from a
      `_loadGeneration`-aborted path) able to overlap with a different,
      already-in-flight generation's `initialize()`, reusing the same GL
      object id mid-teardown. Fix: `client/lib/util/gl_serial.dart`'s
      `serializeGlOp` — a single shared FIFO chain every `initialize()`/
      `dispose()` call in `main.dart` and `mosaic_page.dart` now goes through,
      so the native GL work is strictly ordered regardless of which unrelated
      Future chain (a channel-switch generation, the mosaic preview, ...)
      enqueued it (see `AGENTS.md`'s gotchas).
      Alongside it, added channel-switch coalescing
      (`_channelLoadInFlight`/`_pendingChannelSwitchTarget` in
      `_switchToChannelIndex`): mashing next/prev used to fire one real
      network-fetch-plus-controller-create per keypress, each discarded by
      the next press — wasteful, and the exact scenario that made the race
      above easy to hit. Deliberately adaptive instead of a fixed debounce
      timer (decided with the user: a single, isolated press must stay
      perceptually instant, as before) — a press loads for real immediately
      unless a previous press's load is still in flight, in which case it
      only updates the pending target/visual index; the in-flight load's own
      generation check aborts almost immediately and the pending target loads
      right after.
      While validating this against the real `integration_test` suite (per
      `AGENTS.md`'s operational practices), found and fixed a pre-existing,
      unrelated gap left over from issue #7's Schedule/Playout split:
      `client/integration_test/support/backend_fixture.dart` and
      `client/test/playhead_api_test.dart` still posted to the removed
      `/schedule-entries` endpoint — updated both to create a `Schedule` +
      `ScheduleItem` and assign it via `PUT /channels/{id}` (a full replace,
      so the channel's other fields are preserved explicitly). All 6
      `integration_test/*.dart` files and `test/playhead_api_test.dart` pass
      after both fixes.
- [x] Enriched `GET /epg`, tail groups and a rolling working set in the client.
      Each entry now carries the catalog metadata a client needs (title,
      series title, season/episode numbers, source id `external_id`, the
      item's own `duration_ms`, catalog `item_kind`, the Jellyfin image tags
      and the series id/tag), all additive and optional; `group_tail=1` (also
      on `epg-preview`) replaces each run of back-to-back tail entries by one
      `tail` item carrying every clip (shape in `docs/ARCHITECTURE.md`, "API").
      The response has an `ETag` (revision + body hash), `Cache-Control:
      no-cache` and `X-Epg-Revision`, and answers `If-None-Match` with `304`.
      The client reads titles, source ids and art from the entry (no per-entry
      catalog lookups; the lookup path, bounded and cached per id, only runs
      against a backend without the new fields), keeps the program on air plus
      `AppConfig.workingSetSize` (default 5, `--dart-define=WORKING_SET_SIZE`)
      programs resolved and extends them as it plays, fetching the schedule in
      `epgFetchWindow` slices (2h) rather than hours up front, shows a tail as
      one program in the OSD and the EPG, and builds poster URLs from the media
      server address it already holds. Tests: `backend/internal/api/
      epg_view_test.go`, `client/test/epg_enriched_test.dart`,
      `client/integration_test/epg_window_test.dart` (request counting proxy in
      `support/request_proxy.dart`, including a legacy-backend mode).
      Not done / known: only `tail` is grouped (a long `fallback` run of short
      clips is still one entry per clip); only the program on air and the next
      one are preloaded as controllers.

- [x] Client offline cache: a local SQLite copy of the channel list and every
      channel's guide for the window (every tail clip, titles, art tags, source
      ids) plus an on-disk poster cache with a size cap and LRU eviction, so a
      cold start with the backend down plays. A background syncer (startup,
      every 15 minutes, retry every 30 seconds after a failure) refreshes it
      with `If-None-Match`; the player, guide and mosaic only read the cache.
      The window is `PLAYHEADTV_EPG_WINDOW` on the backend (default 48h,
      exposed as `window_ms` in `GET /epg/meta`) capped by a client limit
      setting; a cache that ran out loops the last known programming or holds
      the slate (client setting, default loop); a changed schedule is applied
      like a grid reset; an auth or network error never discards the cache.
      Settings -> Offline cache holds the settings, "Sync now" and "Clear
      cache", all operable with the six buttons. Design:
      `docs/ARCHITECTURE.md` "Client offline cache". Tests:
      `client/test/{schedule_cache,schedule_sync,art_cache}_test.dart`,
      `client/integration_test/offline_*_test.dart`,
      `settings_offline_test.dart`, `backend/cmd/server/main_test.go` and the
      window tests in `backend/internal/{api,materializer}`.
      Not done / known: the device token is static configuration, so a hosted
      token that expired is only retried every 30 seconds (the cache is kept
      and refreshed as soon as the backend accepts the configured token), never
      renewed; the poster cache holds only the primary image of programs and
      channel logos.

- [x] Client starts in full screen on desktop (setting, on by default), via a
      small window seam (`AppWindow`, `FullScreenController`) and a method
      channel served by the Linux runner; Settings row, config file key and
      environment variable. Design: `docs/ARCHITECTURE.md` "Client window (full
      screen)". Tests: `client/test/app_window_test.dart`,
      `client/integration_test/fullscreen_test.dart`.
      Not done / known: no Windows runner yet, so Windows has no implementation
      of the channel; Android is untested (the setting is a no-op there).

- [x] Client exit flow: Back on the mosaic asks "Exit Playhead TV?" (Cancel
      focused); Exit tears down (preview controller, wake lock, syncer) and ends
      the process with exit code 0; the player's Back chain is unchanged. Behind
      an `AppExit` seam and an `AppExitController` that fixes the teardown order.
      Design: `docs/ARCHITECTURE.md` "Exit flow". Tests:
      `client/test/app_exit_test.dart`, `client/integration_test/exit_flow_test.dart`.
      Not done / known: no optional quit shortcut and no "don't ask again";
      the Android path (`SystemNavigator.pop`, and whether Android TV keeps the
      dialog) is untested.
- [x] Client mosaic fills the screen vertically, navigates spatially, and the UI
      scales with the screen (every screen: mosaic, OSD, EPG, track picker,
      update banner, Settings, Remap controls, Offline cache, dialogs).
      Design: `docs/ARCHITECTURE.md` "Mosaic geometry, navigation and UI scale".
      Tests: `client/test/mosaic_layout_test.dart`, `mosaic_nav_test.dart`,
      `ui_scale_test.dart`, `ui_scale_golden_test.dart`,
      `ui_scale_screens_golden_test.dart` (goldens in `client/test/goldens/`),
      `client/integration_test/mosaic_nav_scale_test.dart`. A fixed Interface
      size is capped by what fits the window.

## Phase 6 — Platforms

- [ ] Android / Android TV client build.
- [ ] iOS/tvOS — deferred, no official Flutter tvOS support (community fork
      only, no WebView). Revisit later.

## Releases and deployment

Design: `ARCHITECTURE.md` "Releases and updates"; procedures: `RELEASING.md` and
`deploy/README.md`.

- [x] Backend version and API version: `GET /version` (public), build version
      injected with ldflags (Dockerfile `VERSION`/`COMMIT` build args), `server
      healthcheck` subcommand for the distroless image.
- [x] CI (`.github/workflows/ci.yml`): backend build/vet/test, web UI build and
      tests, client analyze and unit tests.
- [x] Backend release workflow: `vX.Y.Z` tag, multi-arch image to GHCR with
      immutable tags, GitHub Release.
- [x] Production deployment example (`deploy/`): Compose project, no seeded
      admin, healthcheck, upgrade, backup and rollback procedure.
- [x] Client package (`client/tool/package_linux.sh`, Linux x86_64): bundle,
      launcher, installer, desktop entry, checksums; release workflow on
      `client-vX.Y.Z`.
- [x] Client config file (backend URL, device token, ...) read at start.
- [x] Headless client auto-update with checksum verification, atomic swap,
      rollback of a version that does not come up healthy, a disable switch and
      a `stable` channel.
- [x] Backend compatibility range: the client checks `api_version` and says so
      when it is outside the range.
- [ ] Validate the pipelines and the packaged client on real infrastructure
      (checklist in `RELEASING.md`).
- [x] Signed releases: the checksums file carries a detached Ed25519 signature
      (`CLIENT_SIGNING_KEY` secret), the client pins a list of public keys and
      refuses an update whose signature is missing or invalid; a build with no
      key pinned refuses every update. Key rotation in `RELEASING.md`.
- [ ] Pin the real signing public key in `client/lib/update/pinned_keys.dart`
      and set the `CLIENT_SIGNING_KEY` secret (the list ships empty, so the
      client release workflow fails until then).
- [x] AppImage next to the tar.gz (static runtime, same updater swapping the
      file, previous kept for rollback).
- [x] New-version banner by install kind and a one-time plain-text changelog
      after an update (the GitHub Release body, generated from the commits since
      the previous client tag).
- [ ] Validate the AppImage (no libfuse2, video, hardware decoding, update and
      rollback) and a signed release end to end on real machines (checklist in
      `RELEASING.md`).
- [ ] Client builds for other platforms (Windows, Android); only Linux x86_64
      is packaged.
  - [x] Android: opt-in ExoPlayer engine (Settings > Video player) for devices
    where libmdk shows a black picture; HEVC playback and channel switching
    verified on an Android TV box.
  - [ ] Android: crossfade, gap slate and long-run stability on ExoPlayer still
    need a full manual pass.
- [ ] An in-app setting for auto-update; today it is the `auto_update` key of
      the config file.

## Backlog / cleanup

- [x] GUI integration suites headless in containers
      (`client/tool/run_gui_suites.sh`, `client/test/docker/`,
      `.github/workflows/gui-suites.yml`; `docs/ARCHITECTURE.md` "Headless GUI
      suites in containers"). All suites pass in containers and the workflow
      runs on hosted runners. Left: make the workflow blocking once it has
      proven stable across several runs.
- [ ] Remove the `fvp` `dependency_overrides` git pin
      (`client/pubspec.yaml`) once `wang-bin/fvp#390`/`#391` ship as an
      official pub.dev release.
- [ ] Test `video.decoders: ['CUDA', 'VAAPI', 'VDPAU', 'FFmpeg']` on real
      AMD/Intel hardware (verified with NVIDIA CUDA/NVDEC only; not yet tested on AMD/Intel).
- [x] Decided: keep `BoxFit.contain` (see `_videoFor` in `client/lib/main.dart`).
      Confirmed 2026-09-26 after diagnosing an unrelated Impeller/GL texture
      corruption bug (fixed separately, see the Linux-runner gotcha in
      `AGENTS.md`) that had been visually conflated with the letterbox bars.
      `cover` was rejected because it crops embedded (burned-in) subtitles
      near the bottom edge on sources that have them — a real content
      regression, not just a cosmetic tradeoff. The black bars stay as-is for
      now; a blurred-backdrop treatment (blur the same frame, scaled up,
      behind the letterboxed video) was proposed as an alternative but not
      requested.
- [ ] `plex` / `local_folder` media source types — schema (`media_source.type`)
      and domain types already accommodate both, neither is implemented.
- [ ] Cloud-sync billing/payment integration (~US$9/year reference price,
      also usable as an optional self-hosted donation flow).
- [ ] OSD/on-screen-UI templating system (idea captured 2026-09-26, not
      designed yet): let the channel guide/OSD look be swapped per
      install/channel instead of the one hardcoded look in `main.dart` —
      e.g. an "early-2000s satellite TV" style, a "90s cable box" style (just a
      big channel number on change, no persistent info panel), a dedicated
      preview/guide channel showing upcoming programming. Longer-term:
      let users author their own template (colors/widgets, likely XML or
      similar) rather than picking from a fixed set. `_OsdOverlay`
      (`client/lib/main.dart`, introduced fixing the fvp texture-rebuild
      artifact below) already isolates the OSD from the video render tree
      as its own widget/State — a reasonable structural starting point for
      this, but the actual template/plugin mechanism is still unstarted.
- [x] Client preload lead: configurable (default 20 s, Settings and config
      file), adapts to measured preload times, retries a failed or late preload
      instead of stopping (`docs/ARCHITECTURE.md` "Client preload lead").
      Not exercised against a real slow library, only the delaying fixture
      server.
- [x] Client software dimmer: OSD button (Select toggles a black layer above
      the player page), global level 10-80% (default 50, Settings and
      `dim_level_percent`), level dialog reachable with six buttons
      (`docs/ARCHITECTURE.md` "Client dimmer"). Not checked on a real display.
