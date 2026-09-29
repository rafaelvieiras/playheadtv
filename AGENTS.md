# AGENTS.md — guidance for agents (human and LLM)

## What this is (one line)

Self-hosted simulated linear TV app: the Flutter client assembles each
channel itself (preload+crossfade against Jellyfin), the Go backend only
owns scheduling metadata — it never touches video. See `README.md` and
`docs/ARCHITECTURE.md` for the full design.

## Layout

- `client/` — Flutter, desktop-first (Linux/Windows) today, then
  Android/Android TV. Video engine: `fvp` (libmdk), coupled directly today
  (3 call sites: `main.dart`, `mosaic_page.dart`, `debug_overlay.dart`) —
  see issue #12 for extracting it behind a playback abstraction; don't
  assume that abstraction exists yet.
- `backend/` — Go.
  - `internal/domain` — plain domain types, no logic.
  - `internal/storage` — the `storage.DB` abstraction (SQLite implemented;
    D1 deliberately deferred until the cloud-sync tier is actually worked
    on).
  - `internal/api` — REST handlers, one flat file per resource.
  - `migrations/` — versioned SQL, embedded into the binary via `embed.FS`.
  - `cmd/server` — entrypoint.
- `shared/` — API contracts consumed by both sides (empty so far).
- `docs/ARCHITECTURE.md` — design decisions; read it before touching the
  schema or the scheduling model. `docs/ROADMAP.md` — the actionable
  checklist, the single source of truth for "what's left" — keep it updated
  as you finish a slice, don't duplicate it here.

## Repository conventions (open source)

This is an open-source project. These rules apply to every contributor, human
or LLM, and to every artifact in the repo:

- **English only.** Code, comments, identifiers, UI strings, log/error
  messages, test names, docs, commit messages, issues and PRs are all written
  in English. The only exception is test data that deliberately exercises
  non-English input (e.g. accent-insensitive genre matching), and it must be
  explained by an English comment.
- **Commits are human-owned.** Every commit is authored by, and is the
  responsibility of, one or more humans. Never add AI/LLM/tool co-authorship
  or attribution to a commit or PR — no `Co-Authored-By:` trailer naming a
  tool or model, no "Generated with ..." footer — even if your tooling adds
  one by default. A `Co-Authored-By:` trailer for another human is fine.
  Messages are short and imperative, in English (optionally with a
  conventional prefix such as `fix(client):`).
- **No personal or infrastructure data.** Nothing in the repo (files, tests,
  fixtures, comments, commit messages, docs) may reveal a developer's or
  operator's personal details or setup: real names or e-mail addresses
  (git authorship and `LICENSE` are the only places), absolute home
  directories, hostnames, IPs, ports or paths of someone's machine,
  hardware/GPU/OS anecdotes stated as a local fact ("on my machine ..."),
  private notes/vault references, credentials, API keys, tokens or the
  contents of `.env*` files. Use placeholders (`example.com`, `<path>`) and
  environment variables. If a hardware quirk matters, state it as a
  condition ("on setups with X ...") and link the upstream issue.
- Quick self-check before committing:
  `git grep -nIP '[áéíóúâêôãõçÁÉÍÓÚÂÊÔÃÕÇ]'` and
  `git grep -nIE '/home/|/Users/'` should print nothing except deliberate
  multilingual test data.

## Principles that don't bend

- The backend never decodes or processes video. All channel assembly
  (preload+crossfade) happens on the client.
- Authoring (channel/block/schedule) only happens through the web UI + MCP
  (neither exists yet) — playback clients never create or edit scheduling,
  they only resolve and play it.
- `storage.DB` is the only interface the rest of the backend sees — never
  branch on "self-hosted vs cloud-sync" outside `internal/storage`.
- No auth yet: every API handler runs under one fixed tenant
  (`api.DefaultTenantID = "default"`). Don't build real multi-tenant logic
  until the Auth roadmap item exists.
- The EPG materializer (`internal/materializer`) expands
  `schedule_entry`/`block`/`filler_rule` into `epg_entry` rows via a
  background worker (full-window regeneration, 48h rolling window — known
  v1 tradeoffs documented in `docs/ROADMAP.md`). `GET /epg` and
  `GET /epg/meta` just read the materialized `epg_entry`/`epg_revision`
  rows — don't duplicate schedule-expansion logic in the API layer, and
  don't reintroduce a "materializer doesn't exist yet" assumption.

## Client app structure (mosaic home, EPG, remappable controls)

- Routing is plain `Navigator.push`/`pushReplacement` with `MaterialPageRoute`
  — no named routes table, no `go_router`. Only a handful of static
  destinations (mosaic, player, settings, controls mapping), no deep-linking
  need, so a router package/named-routes table would buy nothing.
- No shared-state package (`provider`/`riverpod`/etc.) — `MosaicPage` does its
  own independent channel-list fetch and preview (a separate, lightweight
  `VideoPlayerController`, not the player's), and the EPG overlay lives
  inside `_ChannelPlayerPageState` (not a separate route) and reaches the
  player's methods directly (`_switchToChannelIndex`, `_epgOpenNotifier`) —
  there's no cross-page state to share, so a `ChangeNotifier` layer would be
  pure ceremony.
- `shared_preferences` (real singleton-backed store, not mockable in
  `integration_test -d linux` — see the gotcha below) persists: last-watched
  channel id (`SettingsService.lastWatchedChannelId`, read once at boot by
  `_InitialRouteResolver` to decide mosaic vs. straight-to-player), a short
  MRU list of recent channel ids (mosaic tiles), and key binding overrides.
- Input goes through `enum InputAction` (`lib/input/input_action.dart`) +
  `KeyBindingsService.resolve(LogicalKeyboardKey)`, never a raw
  `if (event.logicalKey == ...)` check — `defaultKeyBindings` + a per-action
  override in `SettingsService`, remappable from Settings → Remap
  controls. Exception, deliberate: 2D grid focus movement (`MosaicPage`,
  and the EPG grid's left/right time-navigation) reads
  `LogicalKeyboardKey.arrowLeft/Right` raw, bypassing `InputAction` — grid
  navigation is a different concept from the player's repurposed
  up/down-as-channel-switch semantics and isn't something a user would
  sensibly remap.
- **6-button baseline**: every user-facing flow in the client (not the web
  UI, which is desktop/mouse admin tooling by nature) must be fully
  operable with exactly six inputs — Up, Down, Left, Right, Select, Back —
  the common baseline of any D-pad remote (Mi Box, Android TV, Fire TV,
  Roku all ship this same minimal set). No action may *require* a key
  outside that set by default; on-screen icons/hints and toast feedback are
  affordances/confirmation only, never the only way to trigger something.
  Remapping an `InputAction` to an extra key (`KeyBindingsService`) is how a
  user with a richer input device (a keyboard, a remote with extra buttons,
  e.g. an old set-top-box remote with a dedicated subtitle/audio button)
  gets a shortcut — always optional convenience layered on top of the
  6-button path, never a substitute for it.
  - **Accelerator exception**: an action that only shortcuts an outcome
    already fully reachable via the 6-button path doesn't need a
    6-button-only UI of its own. `InputAction.digit0`-`digit9` (typing a
    channel number) is the reference case: no on-screen numeric keypad
    exists, and none is needed, because channel-by-channel switching
    (`channelNext`/`channelPrevious`) or opening the EPG and selecting a
    channel already reaches the exact same outcome with just the 6 buttons
    — digits are a bonus for desktop keyboards, remotes with a keypad on
    the back, or old remotes with number buttons, never a requirement. An
    accelerator exception only holds when (1) a 6-button-only path to the
    *exact same outcome* already exists and isn't itself broken, and (2)
    nothing is lost by never using it — no screen, feature or configuration
    becomes unreachable without it. If either condition doesn't hold, it's
    not an accelerator, it's a 6-button gap — fix the gap, don't document
    an exception around it.
  - **Definition of done** for any new client feature: before calling it
    complete, verify it's operable start-to-finish using only the 6
    buttons, with zero remapping — unless it's a declared accelerator per
    the exception above, in which case verify the *non-accelerator*
    equivalent path is the one actually exercised in testing.
- `select`'s default binding is `Enter` (not `I`) with two stages, read off
  `_osdVisibleNotifier`/`_epgOpenNotifier`: OSD hidden → show OSD; OSD
  already visible → open the EPG. `back` (`Esc` by default) closes the EPG
  if open.
- The full-screen EPG (`lib/widgets/epg_overlay.dart`) is a `Stack` overlay
  inside the player's own `build()`, gated by `_epgOpenNotifier` — never a
  separate route. This is what lets the active channel's video thumbnail
  inside the EPG reuse the exact same `VideoPlayerController`s already
  playing (just resized into a corner via `AnimatedPositioned`), instead of
  creating a second video surface — see the GL-texture gotcha below for why
  that distinction is load-bearing, not just tidiness.

## Operational practices

- **Always commit when you finish a slice of work** (a feature, a fix, a
  roadmap slice) — don't leave tested, working changes sitting in the
  working tree waiting to be asked for a commit.
- Before calling anything done: `cd backend && go build ./... && go vet ./...
  && go test ./...`, and if you touched HTTP/storage, actually run the real
  binary (`go run ./cmd/server` or `docker compose up`) and hit the
  endpoints — tests passing isn't the same as the feature working.
- UI changes in `webui/` (React) require a real Playwright pass, not just
  `npm run build`/`tsc --noEmit`. Run it against an isolated backend
  instance (never the long-lived `docker compose` one on port 9394) — its own
  admin bootstrap, login, its own test data.
- Feature/UI changes in `client/` (Flutter) require a real pass of the
  `client/integration_test/` suite (harness in
  `client/integration_test/support/`), not just `flutter analyze`. Run it
  with `fvm flutter test integration_test/<file>.dart -d linux`, against
  your own isolated backend instance plus a fixture video server (no real
  Jellyfin needed). Unlike simulating keyboard input via `xdotool` at the OS
  level, these events go straight through Flutter's test binding, so they
  don't depend on the desktop session accepting synthetic input. When adding a widget a
  test needs to find reliably, give it a stable `Key` — dynamic text is a
  brittle finder.
- Never test `client/`/`webui/` against the long-lived `docker compose`
  instance (port 9394), and never write to the named volume
  `playheadtv-data` — always spin up your own isolated instance
  (`PLAYHEADTV_DB_PATH=<temp> PLAYHEADTV_ADDR=:<free port> go run
  ./cmd/server`), populated through the real REST API (admin bootstrap,
  login, fixtures).
- Backend default port is **9394**, not 8080 (8080 is commonly taken by
  other local services).
- Docker: `docker compose build && docker compose up -d`, `docker compose
  logs backend`, `docker compose down`. The named volume `playheadtv-data`
  persists the SQLite file across restarts.

## Gotchas already hit once

- SQLite (`modernc.org/sqlite`, pure Go, no CGO) is single-writer:
  `sqlite.go` already caps the pool at `SetMaxOpenConns(1)` — don't remove
  that thinking it's a forgotten optimization.
- The migration runner does a naive split of statements on `;`. A `--` line
  comment can itself contain a semicolon (e.g. "-- IANA name; resolves ..."
  in `0001_init.sql`), so `splitStatements` strips line comments before
  splitting. A new migration with a comment like that is already handled —
  no need to rephrase the comment.
- `internal/storage/scan.go` maps a Go field to its column via automatic
  CamelCase→snake_case. When a domain field's name doesn't spell out to its
  column (e.g. `FillerRule.Scope` vs column `scope_type`), the `SELECT`
  needs an explicit `AS` alias — see `fillerRuleColumns`/`scheduleEntryColumns`
  in `internal/api` for the pattern.
- The client's video engine (`fvp`) has a `dependency_overrides` git pin in
  `pubspec.yaml` waiting on `wang-bin/fvp#390`/`#391` to ship as an official
  release — don't drop it without checking whether that landed.
- The long-lived dev `docker compose` instance always
  seeds `admin@example.com` / `admin` on first boot after a fresh volume —
  `docker-compose.yml` sets `PLAYHEADTV_SEED_ADMIN_EMAIL`/
  `PLAYHEADTV_SEED_ADMIN_PASSWORD`, and `api.SeedAdminUser` (called from
  `cmd/server/main.go`) creates that `app_user` if the tenant has none yet.
  This is intentionally opt-in via env var and never wired into a real
  cloud-sync deployment — don't copy those two vars anywhere outside this
  file. Still never write straight to the running container's SQLite file
  for anything else (e.g. changing programming data by hand) — go through
  the API instead.
- The Linux runner (`client/linux/runner/my_application.cc`) explicitly
  disables Impeller (`fl_dart_project_set_enable_impeller(project, FALSE)`),
  forcing the Skia/GL backend. This isn't an oversight — Impeller's OpenGLES
  path tears/corrupts the `fvp`/libmdk video texture under real playback
  load (reproduced 2026-09-26: a torn frame mid-clip with no channel
  switch involved, and a hard-edged split between two stale frames right
  after a crossfade). Don't remove this to chase an Impeller-only feature
  without re-testing real playback first — the Skia backend is deliberate
  here, not the Flutter default.
- Rebuilding `_ChannelPlayerPageState` (any `setState()` on it) while the two
  crossfade `VideoPlayerController`s exist risks the same class of GL
  texture bug above, reproduced once just by toggling OSD visibility with no
  channel switch involved. The fix, used every time visibility/position of
  something video-adjacent needs to change, is `ValueNotifier<bool>` +
  `ValueListenableBuilder` with the video/overlay widget passed via the
  builder's `child:` parameter (never reconstructed by the builder callback)
  — see `_osdVisibleNotifier`/`_epgOpenNotifier` and the EPG's corner
  thumbnail (`AnimatedPositioned` driven this way, video widgets as a single,
  never-removed Stack child). Don't reach for `setState`/a plain `bool` for
  this class of state, even for something that looks like harmless UI toggle
  state.
- `shared_preferences` in `integration_test -d linux` is the real,
  file-backed store (the test runs the actual app process on the real
  desktop) — `SharedPreferences.setMockInitialValues` has no effect there.
  Any suite whose behavior depends on it (last-watched channel, key
  bindings) needs its own `setUp` that explicitly clears/seeds the real
  store, or it'll leak state between `testWidgets` cases in the same file —
  see the `setUp` in `channel_navigation_test.dart`/`mosaic_test.dart` for
  the pattern.
- Every `VideoPlayerController` sets `avsync.audio=0` (`_createController` in
  `client/lib/main.dart`, and the mosaic preview). By default mdk presents
  video frames against the AUDIO clock; when that sync hiccups the picture
  freezes on one frame while audio keeps playing normally (reproduced
  2026-09-28 on every channel/decoder/renderer; Flutter's own FPS stays
  high during it, so it's invisible to `FrameTiming`). Same mechanism as
  upstream `wang-bin/mdk-sdk#179`. The value must be `"0"`, not `"false"`:
  mdk `stoi()`s it and `"false"` aborts the process with
  `std::invalid_argument`. Don't remove this without re-testing long
  playback for random frame freezes.
- Press `D` in the player (`InputAction.debugOverlay`, remappable) for the
  debug overlay (`client/lib/widgets/debug_overlay.dart`): codec/bitrate/
  buffer from `getMediaInfo()`, Flutter FPS/jank via `FrameTiming`, process
  CPU/RSS from `/proc/self`. Note what it can NOT see: a frozen video
  texture with a healthy compositor. Don't use it to rule out that class of
  bug.
- `channel_navigation_test.dart`'s "typing an existing channel number
  switches channel" case is
  intermittent on a clean checkout (5s window after the 1.5s digit debounce
  is tight). Not caused by client changes — verify against a clean stash
  before chasing it.
- Several real `PlayheadSpikeApp` boots (`tester.pumpWidget`, each with real
  dual crossfade `VideoPlayerController`s and real GL textures) in ONE
  `flutter test` process — i.e. several `testWidgets` in one
  `integration_test/*.dart` file — can intermittently stall a *later* boot's
  rendering pipeline indefinitely (same GL-texture/mdk-under-load class as
  the gotchas above, not a logic bug: reproduced 2026-09-29 at a different
  boot count each time — the 3rd, 4th and 5th real boot in one process each
  stalled at least once — and confirmed to also happen on a clean,
  unmodified stash of `main` when the machine is already under load from
  repeated test runs, e.g. heavy swap usage). A single isolated test (or a
  single real app session doing the same actions back-to-back, e.g. a live
  `setAudioTracks` call followed by a real channel switch) is NOT at risk —
  this is specific to tearing down and rebuilding the whole Flutter
  engine/app several times in one OS process, which never happens for a real
  user (the app only boots once per process lifetime). Keeping each
  integration test file to 2-3 real app boots has kept it reliable in
  practice (see `track_selector_test.dart`/
  `track_selector_persistence_test.dart`, split for exactly this reason);
  don't chase this as an application bug, and don't grow one file's real-boot
  count without budgeting for it.
