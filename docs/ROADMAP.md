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
      (`X-Emby-Token` header, paginated) directly, maps Movie/Episode items
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
      the last one), tagging every entry `kind=program`. A dynamic block's
      preroll still fires once at the top of the occurrence; midroll/
      postroll/tail don't apply (no single "end" to hang them off an endless
      loop). `catalogsync.Delete` also removes any orphaned `block_item`
      before deleting a `catalog_item`, for the same FK reason as
      `filler_pool_item`.

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
      (title substring search, default/max limits 50/200, same as REST),
      `create_block` + `add_block_item` (position always `MAX(position)+1`,
      no reordering — same v1 limitation the web UI already documents above)
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
      add an item to the end, and a remove button per item — reordering
      isn't supported by the API (`POST /blocks/{id}/items` always appends),
      so the UI doesn't pretend otherwise; the known workaround (remove +
      re-add) is left as-is, not solved in this slice. The schedule entry
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

## Phase 6 — Platforms

- [ ] Android / Android TV client build.
- [ ] iOS/tvOS — deferred, no official Flutter tvOS support (community fork
      only, no WebView). Revisit later.

## Backlog / cleanup

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
