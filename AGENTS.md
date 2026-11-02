# AGENTS.md — guidance for agents (human and LLM)

## What this is (one line)

Self-hosted simulated linear TV app: the Flutter client assembles each
channel itself (preload+crossfade against Jellyfin), the Go backend only
owns scheduling metadata — it never touches video. See `README.md` and
`docs/ARCHITECTURE.md` for the full design.

## Layout

- `client/` — Flutter, desktop-first (Linux/Windows) today, then
  Android/Android TV. Video engine: `fvp` (libmdk) behind the app-owned
  `VideoEngine`/`VideoPlayback` seam (`lib/video/video_engine.dart`, the only
  implementations `FvpVideoEngine` (default) and the opt-in Android
  `ExoVideoEngine`; `docs/ARCHITECTURE.md` "Client video engine"). Only
  `lib/video/` imports `fvp`/`video_player`
  (`test/video_engine_boundary_test.dart` enforces it): everything else uses
  `VideoPlayback`, `VideoSurface`, `MediaTracks` and `PlaybackState`.
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
  The rule covers everything published about the project, not only files:
  issues, issue and PR comments, PR descriptions, release notes, and their
  edit history. It also covers inference, not just literal values: never say
  or imply which hardware, operating system, hosting, tooling or deployment
  the maintainers or any user runs. Describe a platform or tool only as a
  supported target or a generic option ("a Linux x86_64 client", "a
  Compose-aware GitOps manager"), never as "the one in use". Write issues
  from the project's point of view, not from a person's setup. If a
  decision depends on someone's setup, record the decision itself, not
  whose setup it is.
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
- A new web UI capability ships with its MCP tool (`internal/mcpserver`, map
  the route in `RouteTools`): `TestEveryRESTRouteHasAnMCPTool` fails on a REST
  route with neither a tool nor a justified `NoMCPCounterpart` entry.
- `storage.DB` is the only interface the rest of the backend sees — never
  branch on "self-hosted vs cloud-sync" outside `internal/storage`.
- `client/lib/plugin/` is the client half of that host: it installs an artifact
  from the backend's registry mirror (SHA-256 verified before extraction, tar
  with traversal/size guards, staged install with an atomic swap) and drives the
  process over protocol 1. It never lists a catalog (the backend owns the grid)
  and never trusts plugin output. A resolve answer arrives in a **mode**:
  `single`, `pair` (the player merges two tracks; the default, and free) or
  `remux` (the plugin produces the stream itself, offered only where the engine
  cannot merge). No path in the client may transcode, and none may name a site.
- `EpgEntry.requires` (from `GET /epg`, kept in the offline cache) is how the
  client learns a channel needs a plugin. A channel whose plugin is missing or
  too old is hidden, never tuned into an error.
- **No site-specific source code, ever.** Some sources (a video-sharing site,
  anything that needs an extractor) are not implemented here: they are
  external plugins, and the core only implements the host half — install,
  verify, run, JSON-RPC (`docs/PLUGINS.md`, `backend/internal/pluginhost`).
  A change that would put URL patterns, extractor logic or that site's name in
  the polling/decode path belongs in the plugin repo instead. This is a legal
  and store-publication boundary, not a style preference.
- No auth yet: every API handler runs under one fixed tenant
  (`api.DefaultTenantID = "default"`). Don't build real multi-tenant logic
  until the Auth roadmap item exists.
- Catalog queries live in `collection`s: dynamic blocks, filler pools and
  criteria items reference one by `collection_id` and never carry an inline
  query. The old `criteria` columns are deprecated snapshots, read only for a
  row still held for review (`docs/ARCHITECTURE.md` "Collections").
- The EPG materializer (`internal/materializer`) expands
  `schedule_entry`/`block`/`filler_rule` into `epg_entry` rows via a
  background worker (full-window regeneration, rolling window of
  `PLAYHEADTV_EPG_WINDOW`, 48h by default — known v1 tradeoffs documented in
  `docs/ROADMAP.md`). `GET /epg` and
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
  controls. Exception, deliberate: menu/grid focus movement (`MosaicPage`,
  the EPG grid's left/right time-navigation and up/down row navigation, the
  track picker's up/down and left/right) reads
  `LogicalKeyboardKey.arrowUp/Down/Left/Right` raw, bypassing `InputAction` —
  menu navigation is a different concept from the player's repurposed
  up/down-as-channel-switch semantics (`channelNext`/`channelPrevious`, which
  must never drive a menu: Up always moves visually up) and isn't something a
  user would sensibly remap.
  Hardware keys that mean the same thing as a default are aliases inside
  `resolve` (`selectKeyAliases`, `backKeyAliases`: the Android remote Back is
  `LogicalKeyboardKey.goBack`, never Esc), so every screen gets them for free;
  never compare against `defaultKeyBindings[...]` or a key constant in a page.
  `BackKeyGuard` (above the Navigator) consumes any Back a page left unhandled,
  so the platform default (finish the activity) can never skip the exit dialog;
  a new page that reacts to Back still handles it itself.
- The mosaic frame fills the screen height (`lib/mosaic_layout.dart`) and its
  focus moves spatially (`lib/mosaic_nav.dart`, pure: the opposite arrow returns
  to the origin, empty slots are skipped). Text, icons, paddings and focus rings
  are written as pixels at 1080p and scaled with the app-wide `UiScale`
  (`lib/ui/ui_scale.dart`, `context.ui(px)`, Settings override); a new screen
  uses it instead of fixed sizes (the EPG, track picker and update banner too;
  the EPG's thumbnail rect is scaled with the same factor). A fixed Settings
  choice is capped by `maxFitUiScale` so it cannot overflow the OSD; keep
  `uiScaleFitHeight`/`uiScaleFitWidth` in step if the OSD grows. Never drive it
  through `setState` on the player page.
- A page-level wrapper that only catches Back (`Focus(onKeyEvent:)`,
  `KeyboardListener`) is non-focusable (`canRequestFocus: false,
  skipTraversal: true`), else it takes traversal focus past the last row
  (`docs/ARCHITECTURE.md` "Client logging", focus conventions); give the page an
  `autofocus` row instead.
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
- The dimmer (`lib/dimmer.dart`, `lib/widgets/dimmer_overlay.dart`) is a black
  layer, the last child of the player's `Stack`, driven by `Dimmer.active`/
  `Dimmer.level` notifiers (never `setState`). In OSD focus mode Select on its
  button toggles; while dimmed a level chip beside it opens the level dialog
  with Select. Holding Select is only an accelerator to that dialog. The level
  is persisted, the toggle is not (`docs/ARCHITECTURE.md` "Client dimmer").
- The watermark (`lib/widgets/watermark_overlay.dart`) is a pointer-transparent,
  never-focusable layer above the video and below the OSD, driven by
  `_watermarkNotifier` (set by `_syncWatermark` wherever `_currentIndex`
  changes, cleared on the gap slate and on a channel switch), never `setState`.
  The backend resolves it per entry; the client draws `program.watermark` and
  never decides inherit/disable (`docs/ARCHITECTURE.md` "Watermark").
- The OSD never describes a filler: for a hidden-kind entry on air it shows the
  program the guide attributes it to, over that program's displayed span
  (`lib/osd_subject.dart`, same `buildGuideCells` rule as the EPG).
- A channel logo is an uploaded image (`PUT /channels/{id}/logo`, bytes on the
  `channel` row, served at `GET /channel-logos/{image_id}/image` and readable
  with a device token) drawn **in place of the channel name**: `number [logo]`
  in the guide's channel column, the logo instead of the name in the OSD header
  (which drops its leading guide-art badge there). `ChannelLogo`
  (`lib/widgets/channel_logo.dart`) resolves it through the art cache — the
  syncer prefetches one file per channel with the device token — and a channel
  without one keeps the text name; a logo that cannot be loaded falls back to
  the name, never to an empty space. Reach for `channelLogoUrl(channel)`
  instead of building the URL by hand, and keep the widget findable by a stable
  key (`epg-channel-name-<id>`, `osd-channel-name`). See
  `docs/ARCHITECTURE.md` "Channel logo".
- The always-on clock (`lib/clock.dart`, `widgets/clock_overlay.dart`) is the
  OSD's time kept on screen with the OSD closed: top-right, above the dimmer
  and the guide. It steps aside while the OSD or the guide is open (they draw
  their own), and its opacity and size are Settings choices
  (`clock_opacity_percent`, `clock_scale`) seeded into `ClockOverlaySettings` at
  boot like `Dimmer.level`. Never drive it with `setState` on the player page.
  See `docs/ARCHITECTURE.md` "Client always-on clock".
- `select`'s default binding is `Enter` (not `I`). The channel OSD
  (`lib/widgets/osd_overlay.dart`) has three states, read off
  `_osdVisibleNotifier`/`_osdArmed`/`_osdFocusNotifier`: merely *displayed*
  (auto-show at channel start/switch — Up/Down still switch channel),
  *armed* (opened by `select` with the OSD hidden) and *focus mode*. From
  armed, the next raw arrow enters focus mode and is applied as the first
  focus move, while a second `select` instead opens the full EPG. In focus
  mode the raw arrows (not remappable) move focus across the CC/audio
  buttons and the "Channels" section (recently watched channels, `Down`
  reaches it), `select` activates the focused one — CC and audio each open
  their own single-panel track picker, a channel card switches to it — and
  `back` hides the OSD. Controls with no behavior yet are dimmed and skipped
  by focus. Focus mode ends whenever the OSD hides. `back` (`Esc` by
  default) closes the EPG if open, hides an armed OSD, and otherwise goes to
  the mosaic. On the mosaic `back` opens the "Exit Playhead TV?"
  dialog (Cancel focused; `AppExitController` tears down, then `AppExit`
  terminates, never a direct `exit()`): the player never exits directly.
  `InputAction.trackSelector` has no default binding anymore (optional
  accelerator; opens the two-panel picker).
- The client reads `GET /epg?group_tail=1` (enriched entries, a tail as one
  item carrying its clips, see `docs/ARCHITECTURE.md`) and never looks up
  catalog items per entry: titles, source ids and art tags come with the entry
  (`EpgEntry` in `playhead_api.dart`; every enriched field is optional and
  `PlayheadApi.resolveCatalog` is the bounded, cached per-id fallback for a
  backend that lacks them). What it KNOWS (`_known`) is fetched in
  `epgFetchWindow` slices as it plays; what it holds ready (`channel`, the
  working set) is the program on air plus `AppConfig.workingSetSize` programs
  (default 5), topped up by `_expandWorkingSet` after every item change. A
  program is one guide item and may be several playable units (`ChannelEntry`):
  a grouped tail is one program, one unit per clip, announced once by the OSD.
  Don't reintroduce a per-entry `getCatalogItem` loop or load the whole window
  into `channel` up front.
- Everything behind the video (letterbox bars, loading screen, crossfade, gap
  slate) is pure black: the player `Scaffold` sets
  `backgroundColor: playerBackgroundColor` statically (not the dark theme's
  grey scaffold color, and never via `setState`).
- The display is kept awake while the player plays a channel
  (`lib/services/screen_awake.dart`: `ScreenAwakeController` policy over the
  `ScreenAwake` interface, `wakelock_plus` underneath; toggled from
  `_startChannel`, `didChangeAppLifecycleState` and `dispose`, never
  `setState`). Released on leaving the player and when the app is hidden; the
  mosaic's decorative previews never hold it. On Linux it calls the
  `org.freedesktop.ScreenSaver` D-Bus service (`dbus` package; needs a session
  bus and a desktop that serves it: GNOME, KDE, Xfce, ...) and falls back to
  `wakelock_plus`'s portal path; without either it is a silent no-op. Don't
  swap it for the portal alone: on GNOME/X11 that call is accepted but creates
  no inhibitor.
- Nothing plays while the app is not visible. On `hidden`/`paused` the player
  tears the channel down through the channel-switch path
  (`_suspendPlayback` -> `_switchToChannelIndex(load: false)`: timers, preload,
  gap hold and both controllers) and the mosaic drops its preview; on `resumed`
  the channel is reloaded at the point the guide has reached. `inactive` (focus
  lost, window still visible) keeps playing. A hidden app draws no frames
  (`framesEnabled` is false), so a teardown must never wait on `endOfFrame`
  without a bound, and a test cannot wait with `tester.pump` while hidden
  (`integration_test/app_background_test.dart`).
- While a channel loads (boot or switch) the player shows a centered spinner
  instead of a status card; errors keep the plain status card. Each
  `VideoPlayback` is tuned with the read-ahead window
  (`playbackBufferMinMs`+`playbackBufferMaxMs` in `main.dart`, mdk's `buffer`
  property in `FvpVideoEngine.tune`): playback
  waits for the min and the demuxer reads ahead up to the max, to ride out
  bursty latency from network-backed libraries. A min above the remaining
  length of the 8s integration-test clips stalls their channel load — keep
  it small.
- The full-screen EPG (`lib/widgets/epg_overlay.dart`) is a `Stack` overlay
  inside the player's own `build()`, gated by `_epgOpenNotifier` — never a
  separate route. This is what lets the active channel's video thumbnail
  inside the EPG reuse the exact same `VideoPlayerController`s already
  playing (just resized into a corner via `AnimatedPositioned`), instead of
  creating a second video surface — see the GL-texture gotcha below for why
  that distinction is load-bearing, not just tidiness.
  The grid inside it is responsive: a fixed 3h visible span
  (`_visibleSpan`) with pixels-per-minute derived from the available width on
  every build, a sticky time header (tick every 30 min), a "now" line
  refreshed by a timer, rows of fixed height in a vertical `ListView` that
  follows the focused row, and horizontal scrolling kept as one animated
  offset in minutes (not `Scrollable`s) so header, rows and now line stay
  aligned. Up/Down from a live program (its displayed span covers now) land on
  the adjacent row's live program (`guideVerticalTarget`) and set the anchor to
  now, so repeated moves track the on-air column; from a non-live program, or
  when that row has no live cell, they pick the program covering the focus's
  time anchor in the new row (Left/Right re-anchor on the new cell); moving right past the loaded window reads more of the offline
  cache (up to its window, `ScheduleCache.window`: the smaller of the
  backend's `window_ms` and the client limit). All of this state lives in
  `EpgOverlayState`, never in `_ChannelPlayerPageState`.
  The grid draws `GuideCell`s (`lib/epg_guide.dart`, mirrored by
  `webui/src/lib/guideDisplay.ts`), not raw entries: only `program` and
  `fallback` entries get a cell, and the time of hidden preroll/midroll/
  postroll/tail entries is attributed to a neighbouring program (see
  `docs/ARCHITECTURE.md`). Navigation, focus, "live" and the details panel
  all work on the cells, so focus never lands on a hidden entry; the OSD
  "Up next" line skips the same kinds. Keep the two implementations and
  their unit tests in sync.

## Client offline cache (offline first, excluding the video flow)

Design and rationale: `docs/ARCHITECTURE.md` "Client offline cache". What to
keep in mind when touching `client/`:

- The player, the EPG grid, the mosaic and channel switching read the local
  SQLite cache (`lib/cache/schedule_cache.dart`) and NEVER call the backend
  for schedule data; only `ScheduleSyncer` (`lib/cache/schedule_sync.dart`)
  does. `OfflineRuntime.ensure()` is the one shared instance (cache, art
  cache, syncer, `PlayheadApi`); a new reader asks it, it does not build its
  own `PlayheadApi`.
- A failed refresh (backend down, `401`/`403`, any status) must leave the cache
  untouched. Only a schema-version bump (`scheduleCacheSchemaVersion`: bump it
  whenever the tables or the stored entry JSON change shape) or the explicit
  "Clear cache" action empties it. Don't add a wipe on an error path.
- Anything the client reads from the backend at runtime needs a cached copy and
  a graceful fallback when absent (channels: cache; stream rules:
  `StreamPreferencesService`, never caching "unreachable" as an answer).
- The syncer's `GET /epg` starts at the top of the previous hour on purpose, so
  the body and its `ETag` stay the same for the whole hour and an unchanged
  guide is a `304`. A `start` of "now" would make every refresh a download.
- `whenComplete(() => map.remove(key))` deadlocks when `map[key]` is the very
  future being completed (the callback's returned future is awaited): use a
  block body (see `ArtCache.fetch`).
- Reconcile after a refresh (`_reconcileWithCache` in `main.dart`): reload the
  channel when what is on air changed (the "applies it like a grid reset"
  rule), otherwise replace only the programs after the one on air, in place.
  The in-place path leaves `channel[0..current]` untouched (timers and
  controllers hold indexes into it) and is skipped while a preload or gap hold
  is parked.
- The exhausted-cache fallback is the client's own setting
  (`SettingsService.exhaustedBehavior`, default loop). It only applies when the
  backend was just asked and could not be reached (`_backendUnreachable`); a
  reachable backend with nothing scheduled keeps the "nothing more scheduled,
  retrying" behavior.
- In the integration harness: `AppConfig.overrideForTest` resets the shared
  runtime and gives every call its own temporary cache directory (pass
  `cacheDir:` to reuse one across boots, as a cold-start test does);
  `BackendFixture.stopServer()`/`restartServer()` take the backend down and up
  on the same address and data (it runs as a built binary, not `go run`);
  `RequestProxy` records `If-None-Match` and statuses and can answer a forced
  status. The media-server fixture also serves a tiny image for
  `/Items/<id>/Images/...`.
- Key events and OS window focus: when another window takes OS focus from the
  test app (a terminal, a notification), Flutter drops primary focus to the
  root scope and the player page's `KeyboardListener` stops receiving keys
  ("Enter did not show the OSD"), although `HardwareKeyboard` still sees them.
  It is deterministic, not load-related. Suites send keys through
  `pressKey` / `ensurePlayerFocus` (`integration_test/support/key_focus.dart`),
  which re-request the player's node when focus fell to a scope; use them
  instead of `tester.sendKeyEvent`, and call `ensurePlayerFocus` before a raw
  `sendKeyDownEvent`. The app itself re-requests focus on
  `AppLifecycleState.resumed` (`didChangeAppLifecycleState` in
  `_ChannelPlayerPageState`, no `setState`; `focus_lifecycle_test.dart`).
  GUI suites still need a desktop where nothing else takes focus while they
  run, and only one `flutter_tester` at a time.
  Without a desktop, run a suite under `xvfb-run -a -s "-screen 0 1920x1080x24"`
  (the default 640x480x8 screen makes the app fail to start) with
  `CUDA_VISIBLE_DEVICES=-1`: on hosts with a proprietary NVIDIA driver, mdk
  probes the `CUDA` decoder first and segfaults on the raster thread under Xvfb,
  which shows up only as "did not complete" / "Test process is no longer
  needed". A second, software-GL cause of the same symptom: libmdk's
  `VideoShader::update` reads freed memory on the raster thread when Mesa's
  shader disk cache is cold, so the second app boot of a process dies at its
  first channel switch. Run with a warm, persistent `MESA_SHADER_CACHE_DIR` (a
  throwaway `$HOME` has none; `run_gui_suites.sh` keeps one in a volume and primes
  it) and never turn the cache off.
  `client/tool/run_gui_suites.sh [-j N] [suite ...]` runs suites headless, one
  container each, with all of that preconfigured and no shared OS focus (so the
  focus-stealing problem above does not apply); see `docs/ARCHITECTURE.md`
  "Headless GUI suites in containers". It does not replace a manual desktop pass.
- Fixtures: a manual block defaults to list_mode `repeat` and loops its items
  to the end of the window, so a fixture block with a short item list fills the
  whole window with repeats (hundreds of entries). Create fixture blocks with
  `list_mode: "once"` (`BackendFixture.createBlockWithItems` does) unless the
  test is about repeating.
- Keep each real-boot budget in mind: `offline_playback_test.dart` (1 boot),
  `offline_coldstart_test.dart` (2), `offline_reconcile_test.dart` (2),
  `offline_exhausted_test.dart` (2), `settings_offline_test.dart` (1).

## Logging

Design: `docs/ARCHITECTURE.md` "Client logging".

- Client code logs through `Log` (`lib/log/log.dart`, pure Dart), with a
  component tag; never `print`/`debugPrint`. The backend uses
  `internal/logging` (`logging.Errorf/Warnf/Infof/Debugf`) instead of `log.Printf`.
- Never put a secret (API key, device token, `Authorization` value) in a message,
  even though the logger redacts the known ones. `error` and `warn` carry no media
  titles or file paths; `info` and below may carry opaque ids, channel numbers and
  timings.
- Logging only observes: it must not change behavior, add a `setState`, or block
  the UI/raster threads. A message built at a hot call site is a
  `String Function()` so it is only built when the level is on.

## Releases and updates

Design: `docs/ARCHITECTURE.md` "Releases and updates"; steps: `docs/RELEASING.md`.

- `buildinfo.APIVersion` (backend) and `minSupportedApiVersion`/
  `maxSupportedApiVersion` (`client/lib/update/compat.dart`) are the
  compatibility contract; bump the API version only for a change an older client
  cannot cope with. Never compare release versions for compatibility.
- The updater (`client/lib/update/`, `client/tool/updater.dart`) is pure Dart
  (no Flutter imports: it is compiled with `dart compile exe`) and reads the
  release assets by name (`clientPackageName`/`clientChecksumsName`); the
  packaging script and the release workflow must keep producing exactly those
  names. It must never throw to its caller or block the app from starting.
- The launcher (`client/packaging/linux/playheadtv`) stays minimal and
  logic-free; decisions belong in the Dart updater. `client/tool/test_launcher.sh`
  tests it and `install.sh`.
- Pin third-party GitHub Actions by commit SHA (with the version in a comment).
  The backend image has no `latest` tag.

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

## Performance measurement

- `client/integration_test/perf_boot_epg_test.dart` measures boot, opening the
  guide and moving around it (frame timings, CPU, RSS per phase, as JSON). In the
  GUI container (`tool/run_gui_suites.sh perf_boot_epg_test`) it runs in debug
  mode on software GL: only the relative change between two runs means anything.
  On a desktop session `client/tool/run_perf.sh` runs it in profile mode
  (`flutter drive`, which needs `test_driver/`). `WidgetTester.sendKeyEvent` fails
  outside debug builds (it looks the physical key up by `debugName`); the perf
  suite presses keys through `support/perf_keys.dart`.
- A device that cannot run the suite (an Android TV box) is measured with the
  opt-in frame trace, `lib/services/perf_trace.dart`: a build made with
  `--dart-define=PLAYHEADTV_PERF_TRACE=true` appends one JSON line per window
  (`epg_open`, `epg_navigate`) to `perf_trace.jsonl`, and `client/tool/mibox_perf.sh`
  drives the guide over adb and prints them. Without the define it is a no-op.
- `flutter build apk --profile` defaults to versionCode 1; installing over an
  existing install needs `--build-number` equal to or above the installed one, and
  keeps the app's data only while the signing key is the same. Back up
  `shared_prefs` first (it holds the connection settings).

## Gotchas already hit once

- SQLite (`modernc.org/sqlite`, pure Go, no CGO) is single-writer:
  `sqlite.go` already caps the pool at `SetMaxOpenConns(1)` — don't remove
  that thinking it's a forgotten optimization.
- The migration runner does a naive split of statements on `;`. A `--` line
  comment can itself contain a semicolon (e.g. "-- IANA name; resolves ..."
  in `0001_init.sql`), so `splitStatements` strips line comments before
  splitting. A new migration with a comment like that is already handled —
  no need to rephrase the comment.
- `mediaSourceColumns` is duplicated in three packages (`internal/api`,
  `internal/pullsync`, `internal/mcpserver`), each with its own copy of the
  list. Adding a `media_source` column and updating only one of them leaves
  the others scanning NULL into the new field — a plugin source silently
  reported "names no plugin" because `pullsync`'s copy still had the old
  columns (hit when `0028_plugin.sql` landed). Grep for the constant name
  after any `media_source` change.
- A plugin registry/artifact is untrusted input: `internal/pluginhost` verifies
  the archive's SHA-256 before extracting, refuses archive entries that escape
  the target directory, caps extracted size and entry count, and kills a plugin
  process that misses its call timeout. Don't add a path that skips any of
  those because "it's the same host".
- `internal/storage/scan.go` maps a Go field to its column via automatic
  CamelCase→snake_case. When a domain field's name doesn't spell out to its
  column (e.g. `FillerRule.Scope` vs column `scope_type`), the `SELECT`
  needs an explicit `AS` alias — see `fillerRuleColumns`/`scheduleEntryColumns`
  in `internal/api` for the pattern.
- The client's video engine (`fvp`) has a `dependency_overrides` git pin in
  `pubspec.yaml` waiting on `wang-bin/fvp#390`/`#391` to ship as an official
  release — don't drop it without checking whether that landed.
- On setups whose platform decoder rejects libmdk's surface, the `fvp` engine
  plays sound with a black picture and the log shows `MediaCodec: Codec
  reported err 0xfffffc0e` plus `NdkMediaCodec: sf error code: -1010`/`-38`
  from the device's hardware decoder (`OMX.amlogic.*.decoder.awesome` is one).
  The picture never comes back for later channel switches either, while the
  EPG's corner thumbnail still draws frames — the debug overlay's `VIDEO`
  block keeps reporting a codec, resolution and a read-ahead buffer, and the
  position keeps advancing. Restarting the app or the device does not help.
  `Settings -> Video player -> ExoPlayer` (Android only, applies at the next
  start) does; it is the opt-in engine from `docs/ARCHITECTURE.md` "ExoPlayer
  engine", and the debug overlay then reports `ExoPlayer (Media3)` as the
  player. Reach for that switch before treating the symptom as an app bug.
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
- Every `VideoPlayback` is `tune`d, and `FvpVideoEngine.tune` sets
  `avsync.audio=0` (`_createController` in `client/lib/main.dart`, and the
  mosaic preview). By default mdk presents
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
  switches channel" case once failed intermittently, and the cause is the
  test's wait, not the app: measured across 5 runs, the switch after the
  1.5s digit debounce fires (cache read, controller create, first frame,
  OSD shown) takes ~70-100ms. The old explicit 5s bound left ~3.5s after
  the debounce, which a machine busy with another GUI run (native GL/mdk
  init stalls) can exceed. The wait now uses the suite's 15s default
  condition-based bound; don't shrink it back.
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
- The same GL-texture class above escalated to a real segfault (reproduced
  2026-09-30, switching channels in quick succession): `fvp`/libmdk creates
  GL resources (fbo/tex) on its own raster thread during `initialize()` and
  frees them on a separate dispose thread during `dispose()`, with no
  synchronization between the two, and those threads are process-global —
  shared by every `VideoPlayerController` instance, not one pair per
  controller. This is fvp's problem, so it is `FvpVideoEngine`'s to solve. A fast enough channel switch lets a stale controller's
  `dispose()` (fire-and-forget, since the aborting `_loadGeneration` check
  doesn't block on it) overlap with a different, already-in-flight
  generation's `initialize()` for its own controller, reusing the same GL
  object id mid-teardown. The fix: `client/lib/util/gl_serial.dart`'s
  `serializeGlOp` — `FvpVideoEngine`'s `open()` and `dispose()` (for
  every playback: the player's, the slate, the mosaic preview, whose own
  controller hits the same process-global threads) are routed through one
  shared FIFO chain, so the native GL work never overlaps regardless of which
  unrelated Future chain enqueued it. Callers just `await playback.open()` /
  `disposeController(playback)`; a NEW call site needs nothing extra, but an
  fvp playback created outside `FvpVideoEngine` would bring this back. Centralizing every call behind one queue has its own failure mode,
  though: the pre-existing raster-thread stall documented just above (a
  create()/dispose() that never resolves under machine load) would otherwise
  wedge that single shared queue forever, blocking every *other*, unrelated
  controller's GL calls too — strictly worse than before the queue existed,
  when a stalled controller only ever froze itself. `serializeGlOp` bounds
  this with `_glOpQueueTimeout` (20s): the queue stops waiting on a stuck op
  and lets the next one in line start, while the stuck op's own caller still
  gets its real result/error whenever (if ever) it resolves — the timeout
  only protects the queue, it never cancels or misreports the op itself.
- Channel-switch coalescing (`_channelLoadInFlight`/
  `_pendingChannelSwitchTarget` in `main.dart`'s `_switchToChannelIndex`):
  mashing next/prev used to fire one real `_loadAndPlayChannel` (network +
  controller create) per keypress, each one immediately discarded by the
  next press's `_loadGeneration` bump — wasteful, and the scenario that made
  the GL-serialization race above easy to hit. Deliberately NOT a fixed
  debounce timer (that would add latency to the common single, isolated
  press): a press is loaded for real immediately unless a previous press's
  load is still in flight, in which case it only updates the pending target
  and the visual channel index — the in-flight load's own generation check
  aborts it almost immediately, and the pending target loads right after.
  Adapts to real network/controller-creation latency with no magic number to
  tune; don't replace it with a timer-based debounce without re-confirming
  that trade-off. Loading never blocks navigation: a load in flight longer
  than `AppConfig.channelLoadStallBound` (2s) is stalled, and the deferred
  switch is released then (`_armStallRelease`) instead of waiting for a source
  that may never open; the stalled load's late controller is disposed by the
  generation check in `_startChannel`. Over the spinner the OSD is a
  channel-name banner (`loading-osd`) and Select, Select opens the EPG; a new
  stall path must keep all three working (`loading_navigation_test.dart`). The
  GL queue valve (`glOpQueueTimeout`, 20s) still bounds how long a switch can
  wait behind a stuck `initialize()`.
- A catalog item with a non-positive duration (reproduced 2026-09-30: a
  Jellyfin library item synced with `RunTimeTicks: 0`, missing/unprobed
  metadata) breaks the entire channel-assembly model if it ever reaches a
  schedule. `_scheduleTransitions` (`client/lib/main.dart`) fires its
  next-item `Timer` relative to the clip's duration — a zero-length one fires
  essentially immediately, so the client crossfades into the next item right
  away, and if that one is ALSO bad (or the rotation loops back to the same
  one), it cascades into a tight loop of real `_createController`/dispose
  cycles (network connect + native GL lifecycle per iteration), fast enough
  to exhaust the process's file descriptors and crash — observed as
  PulseAudio aborting with "cannot create wakeup pipe" after a burst of
  fbo/tex create+cleanup lines with no user interaction in between, and
  reported by the user as "channel 1 takes a while to load and restarts the
  movie every time" (the cascade churns through a few bad entries before
  landing back on the real movie, repeatedly). Fixed in three layers, don't
  remove any of them: (1) `pullsync/jellyfin.go`'s `mapJellyfinItem` now
  rejects `RunTimeTicks <= 0` at sync time, so bad source metadata never
  becomes a catalog item at all; (2) `_loadAndPlayChannel` and
  `_doFetchNextEpgSlice` (`main.dart`) both filter `GET /epg` entries down to
  `endAt.isAfter(startAt)`, so a bad entry can never enter `channel` even if
  one slips past (1) some other way (e.g. a manual API call, a future
  `TrimDurationMs` bug); (3) `_scheduleTransitions` clamps `effective` to
  `minTransitionInterval` (5s) as a last-resort floor, so even a bug nobody
  has thought of yet can't turn a near-zero duration into a runaway loop.
  Confirmed via a 20-cycle player↔mosaic stress test (real controllers, fake
  video server) that the normal lifecycle itself doesn't leak file
  descriptors — the leak only shows up when something schedules a genuinely
  broken-duration item.
- A `tail` is attributed to the previous program for 30 minutes at most
  (`buildGuideCells`, `client/lib/epg_guide.dart` and
  `webui/src/lib/guideDisplay.ts` — keep the two in step, with the same unit
  cases); past it the guide shows a gap and the OSD names the channel
  (`osdSubjectFor`, which needs `now`). The EPG overlay and the OSD must feed
  `buildGuideCells` the same context (`guideContextLookback` of cache entries
  before their first instant) or they attribute a tail differently. Tests
  shrink the cap with `AppConfig.overrideForTest(tailAttributionCap:)`; never
  wait real minutes. Details: `docs/ARCHITECTURE.md` "Guide display".
- A split item plays as segments (`epg_entry.item_offset_ms`, `split_group`;
  `docs/ARCHITECTURE.md` "Items played in segments"): `ChannelEntry.itemOffset`
  is where in the file the entry starts, so EVERY seek into an entry (a
  preload, a tune-in in the middle, a resync after running behind) is
  `itemOffset + position within the entry` (`_createController`, `_crossfadeTo`),
  and its transition fires at the entry's own `clipDuration`, never the item's.
  Never seek "to the elapsed time" alone. The guide merges the segments of one
  `split_group` into one program in both `buildGuideCells` implementations;
  keep them and their unit cases in step. Only the later segments of an item
  have an offset above 0, which is how the series cursors skip them.
- `ChannelEntry` (`client/lib/main.dart`) carries each item's real,
  materialized `startAt`, and `_crossfadeTo` compares it against wall-clock
  `now` before starting the next item instead of always chaining durations
  back to back — `epgSyncTolerance` (2s) is the slop budget before either
  branch below kicks in. Running behind resyncs by seeking into the item
  instead of starting it from zero (same approach the mid-program tune-in
  boot case already used); an interior schedule gap holds on a looping local
  slate (`_holdForGap`, asset at `client/assets/slate/gap_slate.mp4` —
  attribution in `client/assets/slate/NOTICE.md`) via `_rampCrossfade`, the
  shared opacity/volume stepper factored out of the original crossfade so
  both legs of a hold (outgoing→slate, slate→pending) reuse it. `pending`
  (the real next item, already preloaded independently of the gap by the
  normal fixed `preloadLead` timer) sits outside `_controllerA`/
  `_controllerB` for the whole hold — tracked in `_gapHoldPending` purely so
  a channel switch's teardown (which only disposes whatever is currently IN
  those two slots) can still find and dispose it; forgetting that field when
  touching this code leaks a real controller. Known, accepted trade-off:
  `pending`'s network connection stays open and idle for however long the
  hold lasts — fine for the gap widths seen in practice, revisit (defer its
  creation instead) only if a very long schedule hole turns out to matter.
  Doesn't apply to a gap right at channel tune-in — `_startChannel` still
  starts the first item immediately.
- Updates are authenticated, and the checks have no off switch in a release
  build: the updater verifies the Ed25519 signature of the release's checksums
  file against `client/lib/update/pinned_keys.dart` (an empty list refuses every
  update) before it downloads anything, and anything new the updater fetches must
  be listed in that signed file. The update banner/changelog
  (`lib/widgets/update_overlay.dart`) never takes focus and never rebuilds a page,
  and the release body it shows is untrusted: plain text only, never markup or
  links.
