# NostalgiaTV — Competitor analysis

Documentation of the screens of the **NostalgiaTV** Android app (observed
version: **v0.10.2**), gathered from screenshots of the app, to understand the
feature set of a direct Playhead TV competitor.

Each section below corresponds to one app screen, documented field by field
(name, control type, description, observed value, whether it is a "Pro"/paid
feature). The observations at the end of each screen point out what looks like
a product differentiator, to inform Playhead TV roadmap decisions.

---

## Screen: Channel Customization

Editing a single channel inside the app (example channel observed:
"Pennyodeon", number 1008).

### Channel identity

| Field | Control type | Description/subtitle | Current value | Pro |
|---|---|---|---|---|
| Channel Name (with a favorite/star button next to it) | Text field + action (star) | — | "Pennyodeon" | No |
| Channel Number | Text field (numeric) | — | "1008" | No |
| Channel Visible | On/off toggle | — | Enabled (ON) | No |
| Kid Channel | On/off toggle | "Kid-friendly ratings only." | Enabled (ON) | No |
| Music Channel | On/off toggle | "Audio-only, with a visualizer." | Disabled (OFF) | No |
| Live Stream Channel | On/off toggle | "Plays one HTTP/HTTPS stream URL." | Disabled (OFF) | No |
| 18+ Channel | Toggle (locked until enabled via the profile) | "Turn on Restricted Content in this profile's editor (Profiles → Manage → Edit) to enable." | Locked | No |
| Channel Packs | Expandable dropdown/select | — | "1 packs assigned" | No |

### Content and scheduling

| Field | Control type | Description/subtitle | Current value | Pro |
|---|---|---|---|---|
| Sorting Method | Dropdown/select | Options: Random, Cyclic, Block Shuffle, Block Cyclic, Marathon Shuffle, Marathon Cyclic, Round Robin Shuffle, Round Robin Cyclic, Playlist Order | "Random — Completely random" | No |
| Enable Marathons | On/off toggle | "3% chance to play a show for 4 hours" | Disabled (OFF) | No |
| Max Movies Per Day | Value button | "At most 1 movie scheduled per day" | "1 movie" | No |
| Restrict Ratings After | On/off toggle | "Hold selected ratings until evening hours" | Disabled (OFF) | No |
| Allow Specials | On/off toggle | — | Disabled (OFF) | No |
| Watched Only | On/off toggle | — | Disabled (OFF) | No |
| Deduplicate Content | On/off toggle | — | Disabled (OFF) | No |
| Rent Mode | On/off toggle | "Turns the channel into a video store. Renting is free (just for fun), and commercials never play on this channel." | Disabled (OFF) | No |

### Content source (Add Content)

| Field | Control type | Description/subtitle | Current value | Pro |
|---|---|---|---|---|
| Content Source | Dropdown/select | — | "Add by Collection" | No |
| Plex Library | Dropdown/select | — | "Movies" | No |
| Select Collections | Dropdown/select (multi-select) | — | empty | No |
| Added Content (counter) | Read-only list/section | — | "(0)" | No |

### Profile overrides (per channel)

| Field | Control type | Description/subtitle | Current value | Pro |
|---|---|---|---|---|
| Override Default Language & Subtitles | On/off toggle | — | Disabled (OFF) | No |
| Override Channel Logo Mode | On/off toggle | — | Disabled (OFF) | No |
| Tuning Screen | Value button | "Uses the profile's Tuning Screen setting" | "Profile Default" | No |
| Player | Value button | "Uses the profile's Video Player setting" | "Profile Default" | No |

> No item on this screen shows an explicit lock icon/"Pro" badge — the only
> reference to "Pro" is the "NostalgiaTV Pro" navigation tab next to the
> active tab, not a locked control inside the form.

### Observations — Channel Customization

- The range of "Sorting Method" options is the strongest differentiator on
  this screen: 9 distinct algorithms (Random, Cyclic, Block Shuffle/Cyclic,
  Marathon Shuffle/Cyclic, Round Robin Shuffle/Cyclic, Playlist Order),
  covering everything from fully random selection to sequential binge
  watching and round-robin rotation across series — far more granular than a
  simple shuffle on/off.
- "Enable Marathons" with "3% chance to play a show for 4 hours" suggests a
  probabilistic/stochastic scheduling engine built into each channel, not just
  deterministic rules.
- Three distinct special channel modes — Kid Channel, Music Channel
  (audio-only with a visualizer) and Live Stream Channel (a single HTTP/HTTPS
  URL) — treat "channel" as a broader concept than a video playlist,
  including audio output and rebroadcasting of an external stream.
- "Rent Mode" is a playful reinterpretation of the channel as a "video
  rental store" (free rentals, no commercials on that channel), an
  alternative presentation/UX mode on top of the same content pipeline.
- The overrides (language/subtitles, logo mode, tuning screen, player) show a
  channel-over-profile inheritance model: each channel either inherits
  ("Profile Default") or individually overrides settings that normally live on
  the profile — fine granularity without duplicating the whole profile.
- "18+ Channel" is visible but locked until "Restricted Content" is enabled in
  the profile editor — a two-level content gate (the profile enables the
  category, the individual channel uses it).

---

## Screen: Profile Configuration

User profile settings (observed profile: "demo-user", with "Adult" + "Admin"
badges).

### Profile and sync

| Field | Type | Description/subtitle | Current value | Pro |
|---|---|---|---|---|
| Current Profile | Card + "Choose a different profile" action | — | demo-user — Adult, Admin | No |
| Subtitles | Value button | "Subtitles are hidden" | Off | No |
| Send Playback Status | On/off toggle | "Affects the 'continue watching' section of the media source." | Off | No |
| Mark Media as Watched | On/off toggle | "Media will not be marked as watched" | Off | No |
| Auto-Detect Content Changes | On/off toggle | "App automatically picks up new, edited, and deleted Plex/Jellyfin/Emby items in the background" | On | No |
| Use Plex Recommendations | On/off toggle | "Plex's Recommended rows appear on Movies and TV Shows" | On | No |

### Navigation and playback

| Field | Type | Description/subtitle | Current value | Pro |
|---|---|---|---|---|
| Auto Change Channel on Navigation | On/off toggle | "Channel only changes when you press Select" | Off | No |
| Auto Fullscreen on Startup | On/off toggle | "App opens on the guide view" | Off | No |
| Start on First Channel | On/off toggle | "App resumes the last watched channel" | Off | No |
| On Demand | On/off toggle | "On Demand button and media detail pages are available" | On | No |
| Start on On Demand | On/off toggle | "App starts on the live TV guide" | Off | No |
| Allow Background Playback (Mobile Only) | On/off toggle | "Playback pauses when you leave the app" | Off | No |
| Bottom Navigation Bar (Mobile Only) | Value button | "On mobile: floating icon pill inset from the screen edges, icons only" | Floating | No |
| Swap D-pad Controls for Changing Channels | On/off toggle | "Left/Right changes channels, Up/Down seeks ±10s, long-press Back for previous channel" | Off | No |
| Disable Seeking on Live TV | On/off toggle | "D-pad, remote FF/REW and mobile double-tap seek ±10s on live channels." | Off | No |
| D-pad & Controller Navigation | On/off toggle | "Navigate the on-screen guide and menus with a connected d-pad or game controller, in addition to touch" | Off | No |
| Override Channel Schedule | On/off toggle | "Always play what's currently airing" | Off | No |
| Show Player Controls | On/off toggle | (description cut off by scrolling) | Off (apparent) | No |

### Visual guide/EPG

| Field | Type | Description/subtitle | Current value | Pro |
|---|---|---|---|---|
| Guide Idle Action | Value button | "Guide scrolls automatically after 1 minute of inactivity" | Auto Scroll | No |
| Guide Free Scroll | On/off toggle | "Guide only scrolls when focus leaves the visible area (better performance)" | On | No |
| Channel Logo | Value button | "Channel logo only shown with channel info overlay" | Off | No |
| Show TV Rating at Start | On/off toggle | "Program rating appears briefly when a show or movie starts" | On | No |
| Animate Channel Logo | On/off toggle | "Channel logo stays static" | Off | No |
| Logo Position | Value button | "Top Right" | TR | No |
| Logo Opacity | Value button | "70% (default)" | 70 | No |
| Fullscreen Overlay Style | Value button (probable) | not captured (cut off by scrolling) | not visible | No |
| Player Overlay Elements | Value button | "Choose which info elements appear on the fullscreen channel overlay" | All Elements | No |
| Tuning Screen | Value button | "Classic CRT scan lines while a channel tunes" | Classic CRT | No |
| Channel Column Style | Value button | "Logo beside the number and name in the guide" | Numbers + Logos | No |
| Detailed EPG | On/off toggle | "Two-line info with episode details and movie badges" | On | No |
| Dim Ended Programs | On/off toggle | "Programs that have ended are greyed out in the guide" | On | No |
| EPG Time Indicator | On/off toggle | "Vertical bar shows current time position on the guide" | On | No |
| Scrolling EPG Titles | On/off toggle | "Titles are clipped when they don't fit" | Off | No |
| 24 Hour Clock | On/off toggle | "Time displays use 12-hour format (e.g. 8:00 PM)" | Off | No |

### Video and audio

| Field | Type | Description/subtitle | Current value | Pro |
|---|---|---|---|---|
| Disable Dolby Vision | On/off toggle | "Dolby Vision enabled (may cause issues on some devices)" | Off | No |
| Autoplay Trailers | On/off toggle | "Trailers auto-play when browsing On Demand" | On | No |
| Aspect Ratio | Value button (locked) | "Pro feature - unlock to enable CRT display modes" | Locked | **Yes** |
| Zoom | Value button (locked) | "Pro feature - unlock to enable CRT display modes" | Locked | **Yes** |
| Auto Sign-Off | Value button | "Disabled - broadcast plays continuously" | Off | No |
| Transcode Resolution | Value button | "Max resolution for media playback" | Automatic | No |
| Preferred Version | Value button | "Default version when an item has multiple qualities" | Auto | No |
| Video Player | Value button | "Player engine (applied when you return to the guide)" | ExoPlayer | No |
| Audio Normalization | On/off toggle | "Audio plays at original volume levels" | Off | No |
| Auto Transcode DTS Audio (Plex Servers Only) | On/off toggle | "DTS audio tracks are transcoded so they play on all devices" | On | No |
| Disable Sound Effects | On/off toggle | "Click sounds and loading screen audio enabled" | Off | No |
| Audio Codec | Value button | — | AUTO | No |
| Surround Sound | Value button | — | AUTO | No |
| Audio Passthrough | Value button | "Bitstreams Dolby/DTS when the device reports receiver support" | Auto | No |
| Default Language | Value button | "Preferred language for audio and subtitles" | English | No |

### Maintenance

| Field | Type | Description/subtitle | Current value | Pro |
|---|---|---|---|---|
| Import | Action/button | "Import configuration" | — | No |
| Export | Action/button | "Export configuration" | — | No |
| Report Issue | Action/button | "Send a diagnostic report to help troubleshoot problems" | — | No |
| Reset to Default | Action/button | "Remove all customizations and restore default settings" | — | No |

### Observations — Profile Configuration

- EPG/overlay granularity is higher than expected for an app of this size:
  separate controls for logo position (per corner), logo opacity in %,
  channel column style (numbers vs. numbers+logo), "tuning screen" style
  (classic CRT) and which elements appear in the fullscreen overlay — a strong
  aesthetic/nostalgic appeal as a product differentiator.
- Native d-pad and game controller support for navigating the guide/menus
  (not just touch), including a D-pad remap for changing channels and
  long-press for the previous channel — this indicates an Android TV/set-top
  box target, not just phones.
- Transcoding and audio options are very detailed for a "simulated linear TV"
  app: transcode resolution, preferred version, player engine (ExoPlayer,
  explicitly), automatic DTS transcoding (Plex-specific), audio codec,
  surround sound and passthrough — nearly all with an "Auto" option,
  suggesting deep integration with media server metadata.
- At least 2 "Pro" features locked behind a padlock (**Aspect Ratio** and
  **Zoom**, both tied to "CRT display modes") — a freemium monetization model
  focused on advanced visual/nostalgic customization, not on core playback
  functionality.
- The app has been multi-server aware since its design (texts explicitly
  mention "Plex/Jellyfin/Emby"), and it supports import/export of the entire
  profile configuration — this eases migration between devices, something
  Playhead TV (Jellyfin only today) has no documented equivalent for yet.
- Two pieces of information are incomplete because of scroll jumps between
  screenshots: the full description of "Show Player Controls" and the value of
  "Fullscreen Overlay Style" could not be confirmed visually.

---

## Quick comparison with Playhead TV (points to consider for the roadmap)

- **Data source**: NostalgiaTV already supports Plex, Jellyfin and Emby
  simultaneously; Playhead TV currently only implements Jellyfin pull sync.
- **Per-channel scheduling algorithms**: the range of 9 `Sorting Method`
  options plus `Enable Marathons` is much richer than Playhead TV's current
  block/schedule model — it is worth evaluating whether concepts such as
  "Round Robin" or "Marathon" make sense to port to
  `internal/domain`/`catalogsync` (related to the EPG materializer — see
  AGENTS.md).
- **Channel as a broader concept**: Music Channel (audio + visualizer) and
  Live Stream Channel (rebroadcast of an external URL) are channel categories
  that Playhead TV does not model today.
- **Per-channel overrides over the profile**: the "Profile Default" inheritance
  pattern with a one-off per-channel override is a reusable idea for
  `FillerRule`/player configuration in Playhead TV.
- **Profile import/export**: a configuration portability feature that
  Playhead TV does not have.
- **Freemium monetization**: NostalgiaTV locks only 2 cosmetic options
  (Aspect Ratio/Zoom "CRT modes") behind "Pro" — it does not lock content
  sources, sync or scheduling algorithms.
