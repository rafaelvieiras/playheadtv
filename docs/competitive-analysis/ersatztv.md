# ErsatzTV — Feature map, behavior and flows

Documentation extracted from https://ersatztv.org/docs/ (ErsatzTV Legacy).
ErsatzTV is the project that served as the **direct inspiration for the
programming/scheduling side** of Playhead TV — which is why the Scheduling
section received deeper coverage than the others.

General conceptual flow of the product, per the docs' own Introduction:
**Installation → Media → Collections → Channels → Scheduling → Clients**.

---

## 1. Installation, networking and clients

### Installation

Supported methods: Docker (`ersatztv/legacy:latest`/`:develop`, also via
GHCR), pre-built binaries (Windows x64, macOS x64/arm64, Linux
x64/arm64/arm) and an advanced guide for unRAID (with separate branches per
hardware acceleration: standard, VAAPI, nVidia).

- **Docker**: port `8409`, a writable config volume plus media volumes
  (read-only recommended), optional `tmpfs` at `/transcode` to spare the SSD.
  NVIDIA GPUs require the Nvidia Container Toolkit. Running Docker on
  Windows/macOS is possible but unsupported (hardware acceleration breaks).
- **Native Linux/Windows/macOS**: download/extract the release and run the
  executable (Windows/macOS use a tray icon to open the UI/logs/quit). No
  documented OS service integration (systemd/launchd). FFmpeg is an external
  dependency — ErsatzTV maintains its own pinned fork/release
  ("ErsatzTV-FFmpeg", v7.1.1 in the docs).
- **Downgrade is not supported**: schema/config changes are not backward
  compatible; switching from a "develop" build to a "release" build (or from
  a newer version to an older one) requires restoring from a full backup.

### Networking

- Single default port **8409**, which can be split in two via env vars:
  `ETV_UI_PORT` (management + streaming) and `ETV_STREAMING_PORT`
  (streaming only, blocks management).
- **UI authentication via OIDC**: `OIDC__AUTHORITY`, `OIDC__CLIENTID`,
  `OIDC__CLIENTSECRET` are required; compatible with Pocket ID, Keycloak,
  Authelia, Auth0.
- **Streaming protection via JWT**: `JWT__ISSUERSIGNINGKEY` (32 chars);
  clients pass `?access_token=...` in the URL. **Side effect**: enabling JWT
  disables the HDHomeRun endpoints (a trade-off between security and
  compatibility with clients that only speak that protocol, e.g. Plex DVR).

### Clients

Minimalist integration contract: **just two URLs** — M3U (channel playlist)
and XMLTV (EPG) — cover the entire external integration. No client needs a
proprietary plugin.

| Client | Requirement | Setup |
|---|---|---|
| Plex | Requires Plex Pass | Live TV & DVR → Set Up Plex DVR → enter IP:port manually if autodetection fails. Streaming **MPEG-TS** only. EPG via XMLTV. |
| Jellyfin | — | Tuner Type = "M3U Tuner" + M3U URL; TV Guide Data Provider = "XMLTV" + XMLTV URL. |
| TiviMate | — | Add a playlist of type "M3U Playlist"; EPG is usually autodetected. |
| Channels DVR Server | Requires "Channels Plus" | Custom Channels, stream format MPEG-TS, M3U URL + XMLTV, daily refresh. |
| Kodi | "IPTV Simple Client" addon | M3U playlist URL + XMLTV URL in the addon settings. |

### Notes — Setup/Infra/Clients

- The M3U+XMLTV contract is a much cheaper integration surface than
  building dedicated clients — it reuses the native IPTV support already
  present in popular players. Worth considering whether Playhead TV wants to
  offer this as a secondary path, in addition to its own Flutter client.
- Splitting the management and streaming ports, with JWT on the latter only,
  is a pragmatic model of incremental auth — relevant because Playhead TV
  does not have real auth yet (`api.DefaultTenantID = "default"` is fixed
  today).
- HDHomeRun emulation and JWT are mutually exclusive in ErsatzTV — an
  architectural limit to anticipate if Playhead TV ever emulates a network
  tuner.
- The explicit "downgrade not supported" warning is a risk-communication
  practice worth replicating in deploy docs, since Playhead TV also uses
  versioned SQL migrations with no automated rollback.

---

## 2. Content sources: Media, Search, Collections, Lists

### Media sources

Four source types, with an asymmetry in the supported media types:

| | Local | Plex | Jellyfin | Emby |
|---|---|---|---|---|
| Supported types | Movies, Shows, Music Videos, Other Videos, Songs, Images, Remote Streams | Movies, Shows | Movies, Shows | Movies, Shows |
| Metadata origin | NFO (Kodi) or automatic minimum | Plex (all metadata) | Jellyfin (all metadata) | Emby (all metadata) |
| Authentication | N/A (local paths) | Plex OAuth login | API key generated in the admin UI | API key generated in the admin UI |
| Sync direction | N/A | One-way (server → ErsatzTV) | One-way | One-way |
| Default streaming | Straight from disk | Via Plex | Via Jellyfin | Via Emby |
| Alternative streaming | — | Disk (identical mounts or path replacement) | Disk (identical mounts or path replacement) | Disk (identical mounts or path replacement) |

Only **Local** libraries give access to "unconventional" types (images,
remote streams, standalone songs) — dedicated media servers (Jellyfin
included) are restricted to Movies/Shows in ErsatzTV's model.

### Search

Global per-field search (`field:value`, Lucene-style), with fields specific
to each media type (Movies, Shows, Episodes, Seasons, Artists, Music Videos,
etc.). Highlights:

- Relative time fields: `released_inthelast`/`released_notinthelast`,
  `released_onthisday`, `added_inthelast`/`added_notinthelast`.
- `tag_full` is the non-tokenized variant of `tag` (exact vs. fuzzy match).
- Search results can be saved as a **Smart Collection** — the link between
  Search and dynamic collections.

### Collections

Central concept: **"ErsatzTV does not schedule individual media items; only
collections can be scheduled."** Three types:

1. **Manual Collections** — assembled item by item, with no type
   restriction.
2. **Smart Collections** — generated by saving a Search query (or importing
   Trakt Lists); they **update dynamically** as new items come to satisfy the
   criteria.
3. **Multi Collections** — aggregate other Collections/Smart Collections
   (not individual items); typical use: box sets and franchises (MCU, Star
   Trek, etc.).

### Lists / Playlist

An **ordered** list that mixes Collections, Multi Collections, Smart
Collections and "raw" media entities (a whole show, season, episode, movie,
etc.). Difference from a Collection: a Collection is a grouping (pool) for
scheduling; a Playlist is a sequence that is ordered and controllable item by
item, with three options per item: **Playback Order** (Shuffle/
Chronological/Random/Season/Episode), **Play All** (flatten — plays
everything in that item before advancing) and **Show In EPG** (the item's
granularity in the guide).

### Notes — Content sources

- Search → Smart Collection is the most direct differentiator to compare
  against Playhead TV — but it is not greenfield: `filler_pool.criteria` and
  `block.criteria` (commits `a467cd8`/`1093734`) already implement the same
  core mechanism (a saved criterion that automatically re-evaluates against
  the catalog, with no manual re-curation). The real gap is that today this
  is a fixed 4-column field (kind/genres/year_from/year_to) duplicated across
  two tables with no resolver, no field-based search language
  (actor/tag/studio/etc.), and no first-class "Collection" entity reusable
  outside filler/block — see the roadmap issue "Generalize dynamic
  filler_pool/block criteria into a first-class Collection".
- A clear composition ladder: raw item → Collection → Multi Collection →
  Playlist (ordered, controllable sequence) → Schedule item. The "dynamic
  blocks" concept recently added to Playhead TV covers part of this, but the
  "Multi Collection" level (grouping groupings) is missing.
- The rigid rule "never schedule a loose item, always a collection" solves a
  maintenance problem (swapping a collection's content does not require
  rescheduling) that Playhead TV may not have formalized yet.
- `Show In EPG` per playlist item is a relevant design decision for when
  Playhead TV's EPG materializer (already existing, `internal/materializer`)
  gains item-level granularity inside a block/playlist.
- "Streaming straight from the server" vs. "from disk via path replacement"
  is identical across the 3 media servers — path replacement is treated as
  an optional optimization, not the primary mode.

---

## 3. Channels

### Channel Number

Uniquely identifies the channel; accepts **one decimal place** (e.g.
`500.5`), allowing sub-channels to be inserted between two integer numbers
without renumbering the rest — a common broadcast pattern (e.g. 5.1, 5.2).

### Streaming Mode

| Mode | Transcode? | Watermark? | Notes |
|---|---|---|---|
| **HLS Segmenter** (recommended) | Yes | Yes | Best behavior at transitions between items |
| **MPEG-TS** (recommended) | Yes (wrapper around the HLS Segmenter) | Yes | For compatibility with HDHomeRun clients |
| **HLS Direct** (secondary) | No | No | Best performance on weak machines; may glitch at transitions |
| **MPEG-TS (Legacy)** (secondary) | Yes | Yes | Will be removed in a future release |

### FFmpeg Profile

A collection of transcoding settings (codec, resolution, bitrate) applied to
**all of a channel's content** at once — not per item/block.

### Stream Selector Mode

- **Default**: filters audio by Preferred Audio Language/Title, then picks
  the file's default track or the one with the most channels (e.g. 5.1 >
  stereo). Subtitles: filters by Preferred Subtitle Language, then applies
  Subtitle Mode (None/Forced/Default/Any).
- **Custom**: delegates to a YAML rules file (see the Advanced section).

### Logo

Upload of the channel logo, shown in most client EPGs.

### Notes — Channels

- Streaming Mode explicitly exposes the transcode-vs-passthrough trade-off
  per channel, with direct consequences for watermark/performance/
  transitions — it does not map 1:1 to Playhead TV (which does not transcode
  and does preload+crossfade 100% client-side via `fvp`/libmdk), but it is a
  reminder that "transition quality" and "watermark support" are tied to the
  delivery mechanism, not an afterthought.
- A channel-wide FFmpeg Profile (not per block) contrasts with Playhead TV's
  model, which already thinks in blocks — worth deciding the granularity
  explicitly if something equivalent to a "playback profile" turns out to be
  needed.
- MPEG-TS existing only because of HDHomeRun shows that part of this kind of
  app's audience already uses frontends like Plex DVR/Channels DVR.

---

## 4. Scheduling — Classic Schedules and Playouts (the core of the inspiration)

### Schedule vs. Playout — the central separation

> "Playouts assign a schedule to a channel and individually track the
> ordered playback of collection items. If a schedule is used in multiple
> playouts (channels), the channels may not have the same content playing
> at the same time."

- **Schedule = reusable rule**: an ordered list of Schedule Items, which can
  be attached to more than one channel.
- **Playout = concrete, stateful instance**: applies a schedule to a
  specific channel and tracks actual playback progress — two channels with
  the same schedule are not necessarily in sync with each other.

The public documentation **does not describe the playout materialization
algorithm** (how the schedule becomes a concrete sequence with times), nor
the generation horizon, nor reset/rebuild rules — only the concepts/item
types are publicly documented.

### Schedule configuration options (whole-schedule level)

- **Keep Multi-Part Episodes Together** — groups multi-part episodes so they
  play together and in order (only with the "shuffled" playback order).
- **Treat Collections As Shows** — extends that grouping across different
  shows within a collection (covers "crossovers").
- **Shuffle Schedule Items** — shuffles the order of the *schedule items*
  (not of the media items inside them). **Incompatible** with fixed start
  times and with Flood mode.
- **Random Start Point** — starts each schedule item at a random point in the
  collection, not always at the beginning.

### Schedule Item — Start Type

- **Dynamic**: starts immediately after the previous item ends.
- **Fixed — Strict**: always waits for the exact time, even if that pushes it
  to the next day.
- **Fixed — Flexible**: starts immediately if waiting for the time would push
  it to the next day.

### Schedule Item — Collection Types (content source)

Collection, Television Show, Television Season, Artist, Multi Collection,
Smart Collection, Playlist.

### Schedule Item — Playback Order

Chronological, Random, Shuffle (no repeats until exhausted), Shuffle In Order
(groups by origin, chronological within the group, groups shuffled),
Season/Episode (Television Show only).

### Schedule Item — Playout Mode (the central "item type")

| Mode | Semantics |
|---|---|
| **Flood** | Plays forever or until the next item's fixed time. Incompatible with Shuffle Schedule Items. |
| **One** | Plays exactly one media item, then advances. |
| **Multiple** | Plays N items, via Multiple Mode: Count, Collection Size, Multi-Episode Group Size, Playlist Item Size. |
| **Duration** | Plays as many complete items as fit in a target duration (never cuts an item mid-way). |

**Duration** mode settings: **Tail Mode** ((none)/Offline/Filler — what to do
when nothing else fits) and **Discard To Fill Attempts** (attempts to find an
item that fits, with Shuffle/Random playback order).

### Advanced Schedule Item features

- **Fill With Group Mode**: None / Ordered Groups / Shuffled Groups (rotation
  by show/artist).
- **Custom Title**: groups items in the EPG under a custom title.
- **Guide Mode**: Normal / **Filler** (hides the item from the guide and
  visually extends the previous item to cover the time).

### Filler mechanism

Two places: (1) **Tail Mode = Filler** inside a Duration item — fills the
remaining time with a dedicated filler collection; (2) a **schedule item of
its own** interleaved, typically with Guide Mode = Filler so it does not
appear in the EPG. There is no named pre-roll/mid-roll/post-roll concept —
filler is modeled as full schedule items.

### Official Sample Schedules

1. **24/7 Show** — one item, chronological, infinite loop.
2. **24/7 Balanced Shows** — two items alternating shows in a fixed order.
3. **24/7 Shows with Filler** — two items in random order + up to 5 min of
   filler between episodes.
4. **24/7 Show Every 30 Minutes with Filler** — one shuffle item + filler to
   close out round 30-minute slots.

### Notes — Classic Scheduling/Playouts

- **The Schedule (rule) vs. Playout (materialized instance) separation is
  the most transferable point for Playhead TV.** Today the backend only has
  `channel → block → schedule_entry/epg_entry`, without this second explicit
  layer — exactly the gap the roadmap points to (no EPG materializer yet;
  `GET /epg` just reads `epg_entry`/`epg_revision` as they are).
- **Playout Mode (Flood/One/Multiple/Duration) decouples "where the content
  comes from" from "how much/how to play it"** — worth considering this
  separation instead of mixing both dimensions into a single block concept.
- **Duration + Tail Mode solves "fit into the slot without cutting an item"**
  declaratively, with a cascading fallback (filler → offline) — Playhead TV
  may not have this explicit fallback today.
- **Guide Mode = Filler** is a simple solution for "hide filler from the EPG
  without duplicating scheduling logic" — a requirement that will likely
  show up when Playhead TV builds out its EPG materializer.
- **Shuffle Schedule Items × Flood/Fixed start time is a documented
  incompatibility** — a sign that "randomness in ordering" and "infinite
  mode/fixed time" are fundamentally in tension in a scheduling engine;
  anticipate the same tension when designing block rules.
- **The public docs do not cover the real playout materialization
  algorithm** — only the "what" (concepts/types) can be copied, not the "how"
  (that would require reading the source code).
- **No "schedule reset rule" is publicly documented** — recycling seems to be
  implicit (the end of the list wraps back to the first item); there is no
  ready reference pattern for an explicit "playout reset".

---

## 5. Scheduling — Block, Sequential and Scripted (alternative modes)

### Block Schedules

Replaces the linear list with 5 composable pieces:

- **Block** — a fixed-duration container with a list of "block items"
  (Collection Type, Collection, Playback Order, Show In EPG, Disable
  Watermarks). Implicitly runs in Playout Mode "One".
- **Template** — schedules blocks into recurring non-specific periods (e.g.
  "weekday" vs "weekend"), without pinning days/dates yet.
- **Deco** — separates secondary behavior from primary content: Watermark
  (Inherit/Disable/Override), Default Filler, Dead Air Fallback (loop/trim
  until the next scheduled item).
- **Deco Template** — schedules Decos into non-specific periods.
- **Playout Template** — the final layer: combines Block Templates + Deco
  Templates on concrete days/dates, with priority-based resolution when they
  overlap (similar to "alternate schedules").

### Sequential Schedules

Formerly called "YAML schedules" — defined entirely in YAML, not through the
UI. Four components:

1. **Content Sources**: Search, Show (by tvdb/imdb GUID), Collection, Multi
   Collection, Smart Collection, Playlist, **Marathon** (dynamic mix of
   shows/searches, with `group_by` show/season/artist/album).
2. **Sequences**: reusable, re-runnable instruction blocks (`key` + `items`),
   e.g. a "SHOW_WITH_BUMPERS" with preroll/main/postroll.
3. **Reset Instructions**: a restricted subset (`skip_items`,
   `skip_to_item`, `wait_until`) that runs a single time, when the playout is
   created/reset.
4. **Playout Instructions**: `all`, `count`, `duration` (with `trim`,
   `discard_attempts`, `stop_before_end`, `offline_tail`, `fallback`),
   `pad_to_next`, `pad_until`, `sequence` (with `repeat`), plus control
   instructions: `epg_group` (groups instructions into a single EPG entry),
   `graphics_on`/`graphics_off`, `repeat`, `shuffle_sequence`,
   `skip_items`/`skip_to_item`, `wait_until`, `watermark`.

### Scripted Schedules

The most open mode: an **external executable script** builds the playout by
calling ErsatzTV's REST API (its own OpenAPI spec). It receives 4 positional
arguments (API host, Build ID, `reset`/`continue` mode, custom arguments from
the UI). Python support via Docker: `entrypoint.py` + implementation of
`define_content`, `reset_playout`, `build_playout`.

### Notes — Block/Sequential/Scripted Scheduling

- **Multiple modes as a ladder of increasing expressiveness**: Classic
  (simple manual control) → Block (recurring programming bands) → Sequential
  (complex, reusable, declarative patterns) → Scripted (arbitrary logic).
  Each step trades simplicity for power — one does not replace the other.
- **Deco is the piece Playhead TV most needs to consider**: it separates
  content from decorative behavior (watermark/filler/dead-air) independently
  of the block — a direct, mature answer to the same problem that led
  Playhead TV to create "dynamic blocks" recently.
- **Fallback/trim as a first-class primitive in Sequential**: almost every
  duration instruction has native `trim`, `discard_attempts`, `fallback` —
  fitting content into a fixed window is treated as a core scheduler problem,
  not an ad-hoc side effect.
- **`epg_group` solves EPG pollution from bumpers/intro/outro** — a concept
  to consider when Playhead TV expands its materializer.
- **Scripted avoids building a DSL of its own for the extreme case**: it
  exposes its own REST API as the scripting surface instead of generalizing
  Sequential until it covers everything.
- **Trade-off of maintaining 4 modes in parallel**: it multiplies the
  surface of tests/docs/build-engine complexity — ErsatzTV accepts the cost
  so as not to force advanced users to abandon the product.
- **For Playhead TV, the real decision is not "copy the 4 modes"**, but
  whether it is worth introducing an intermediate reuse layer (like
  Template/Deco, or Sequence) before considering anything scriptable —
  today the single `channel/block/schedule_entry` model covers part of what
  Classic+Block provide, but it has no independent Deco, no declarative
  language for reusable patterns, and no extensibility path like Scripted.

---

## 6. Advanced: Custom Stream Selector and Graphics Engine

### Custom Stream Selector

YAML in `config/scripts/channel-stream-selectors`, per channel. An `items:`
list evaluated in order/priority — selection ends at the first item where
**audio and subtitle match simultaneously** (a fallback cascade, not
scoring). Fields: `audio_language` (wildcard, e.g. `"en*"`),
`audio_title_blocklist`/`allowlist`, `audio_condition` (SQL-like expression:
`id`, `title`, `lang`, `default`, `forced`, `codec`, `channels`),
`subtitle_language`, `subtitle_title_blocklist`/`allowlist`,
`subtitle_condition` (+ `sdh`, `external`), `disable_subtitles`,
`content_condition` (`channel_number`, `channel_name`,
`time_of_day_seconds`).

Documented use cases: blocking/preferring commentary tracks (e.g. RiffTrax)
by title; dual-audio anime (English → Japanese+subtitle → any+subtitle → any
without subtitle); rules that depend on time of day/channel.

### Graphics Engine

Composite overlays (image, text, subtitle, video) on top of the content, via
YAML in `config/templates/graphics-elements/` with **Scriban** preprocessing
(templating with variables from the media item and the EPG). Four types:
**Image** (static watermark), **Text** (dynamic text with named styles),
**Subtitle** (via an `.ass` template), **Motion** (video overlay with a
computed `start_seconds` and `end_behavior: loop`). Activation depends on the
scheduling mode: per item (Classic), per block/deco (Block), via
`graphics_on`/`graphics_off` (Sequential), via API calls (Scripted).

### Notes — Advanced

- A design pattern common to both features: **declarative YAML + Scriban
  templating**, evaluated at runtime — a deliberate middle ground between
  "checkbox" and "plugin in code".
- Custom Stream Selector solves exactly what Playhead TV still delegates 100%
  to Jellyfin without logic of its own — a cascade of prioritized rules per
  channel, including cases the media server alone does not cover (blocking by
  title, language fallback, time-of-day rules).
- The **"atomic match"** requirement (audio and subtitle match together in
  the same item) avoids inconsistent states — a pattern to copy conceptually
  into any future custom stream selector.
- Graphics Engine goes beyond pure scheduling — it is overlay composition on
  the rendering timeline, territory that Playhead TV assigns exclusively to
  the Flutter client (preload+crossfade). A reference for "how far to go"
  before the logic stops being just metadata.
- Both features live outside the UI/API authoring flow — they are loose YAML
  on the server's filesystem, with no validation/preview in the UI. A
  limitation Playhead TV can overcome by exposing the same power as
  structured data in the API (via the planned web+MCP authoring), instead of
  a loose YAML file.

---

## 7. Synthesis — decision points for the Playhead TV roadmap

- **Playout materializer**: Playhead TV already has an EPG materializer
  (`internal/materializer`) — what is missing is the Schedule (a rule
  reusable across channels) vs. Playout (a stateful per-channel instance)
  separation itself: today `Block` is already reusable, but `ScheduleEntry`
  ties directly to a `ChannelID`, with no channel-independent "Schedule"
  entity. This is not just theoretical — the selection cursor of
  `fillLoopingBlock` is keyed by `BlockID` alone, so two channels sharing the
  same dynamic block already leak loop position between each other today.
- **Deco (decorative-behavior layer)**: watermark/filler/dead-air as a
  concept independent of the content block — a direct motivation to refine
  the newly created "dynamic blocks".
- **Multi-server**: ErsatzTV already supports Plex+Jellyfin+Emby
  simultaneously; Playhead TV today only has Jellyfin pull sync.
- **Smart Collections (saved search → dynamic collection)**: a partial
  equivalent exists (`filler_pool.criteria`/`block.criteria`), but it is
  fragmented across two tables and has a fixed criterion (genre/year only) —
  it needs generalizing into a first-class "Collection" with field-based
  search.
- **Declarative fallback/trim to fit duration into fixed slots**: not
  explicitly modeled in Playhead TV today.
- **Per-channel customizable stream selection** (language/subtitle/
  conditions): today delegated to Jellyfin with no logic of its own.
- **External integration contract (M3U/XMLTV)**: a secondary distribution
  path to evaluate, independent of the project's own Flutter client.
- **Multiple scheduling modes (Classic/Block/Sequential/Scripted)** is an
  "expressiveness ladder" pattern to keep in mind, but copying all of them is
  not a priority — worth evaluating introducing only the reuse layer
  (Template/Deco/Sequence) before considering anything scriptable.
