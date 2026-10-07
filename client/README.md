# Playhead TV client

Flutter client for Playhead TV, a self-hosted simulated linear TV app. The client
does the actual "TV" work: it fetches the materialized schedule (EPG) for a channel
from the Playhead TV backend, then plays the programs and commercials straight from
your Jellyfin server, stitching them together on-device with a dual-controller
preload + crossfade. The backend never touches video, and there is no server-side
transcoding. See the [root README](../README.md) and
[`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) for the full design.

## What it does today

- **Offline-first schedule**: a background syncer keeps a local SQLite copy of the
  channel list and every channel's guide for the next window (48h by default), plus
  the poster art on disk. Playback, the guide and the mosaic read only that copy, so
  a cold start works with the backend down and playback goes on for about as long as
  the copy lasts. See [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) "Client
  offline cache".
- **Channel playback**: for the current channel it reads the cached guide (titles,
  Jellyfin item ids and art come with each entry), joins the first program
  mid-way if it is already on air, preloads the next item shortly before the current
  one ends and crossfades video and audio between the two `VideoPlayerController`s.
  A schedule refresh that changes what is on air switches to it, like a grid reset.
- **Mosaic home**: a frame of channel tiles around a live preview of the featured
  channel. Shown on first run and when pressing Home from the player; later boots go
  straight to the last watched channel.
- **Channel navigation**: up/down switches channel by `number`, and typing digits
  jumps directly to a channel number, like a real remote control.
- **On-screen guide (OSD)**: channel number and name, current item with real playback
  position, and the next item.
- **Full-screen EPG**: a schedule grid for all channels, with the live video shrunk
  into a corner thumbnail. Holding Enter on a program expands its details.
- **Settings**: **Remap controls** to rebind every key, and **Offline cache**: what a
  channel does when the cached schedule has run out (loop the last known programming,
  the default, or hold the local slate), the cache window limit, sync now and clear
  cache. On desktop, **Start in full screen** (on by default) opens the client in full
  screen before the first frame and toggling it applies immediately. All of them are
  operable with the six baseline buttons.
- **Debug overlay** (`D`): decoder, FPS/jank, CPU/memory, buffer state and detected
  video freezes.

## Platforms

- **Linux desktop**: the only platform that is built and tested today.
- **Windows desktop**: planned. There is no `windows/` runner in this directory yet;
  it must serve the same `playheadtv/window` method channel as the Linux runner (see
  `lib/services/app_window.dart`) for full screen to work there.
- **Android / Android TV**: planned. Only the default Flutter `android/` scaffold
  exists. It has not been built or tested against this app.

The video engine is [`fvp`](https://github.com/wang-bin/fvp) (libmdk) behind
`video_player`.

## Prerequisites

- [FVM](https://fvm.app) with the Flutter SDK pinned in [`.fvmrc`](.fvmrc) (the
  `stable` channel). Run `fvm install` in this directory, then use `fvm flutter ...`
  for every command below. Avoid a snap-installed Flutter: it exports variables that
  break the C++ toolchain `fvp` needs.
- The standard Flutter Linux desktop toolchain (clang, cmake, ninja, pkg-config, GTK 3
  development headers). `fvm flutter doctor` tells you what is missing. `fvp`
  fetches the libmdk SDK during the build, so the first build needs network access.
- A running Playhead TV backend (see the root README), and a Jellyfin server that
  holds the media the schedule refers to.
- A device token for the client (see [Configuration](#configuration)).

`pubspec.yaml` carries a `dependency_overrides` entry that pins `fvp` to a git
branch. `fvp` 0.38.1 on pub.dev has a lifecycle race
([wang-bin/fvp#390](https://github.com/wang-bin/fvp/issues/390)) that breaks the
dual-instance preload/crossfade, and the fix
([#391](https://github.com/wang-bin/fvp/pull/391)) has not shipped in a release yet.
Do not drop the override before checking that it has.

## Configuration

Each value comes from, in order: a `--dart-define` (a developer build), then the
**config file**, then the default. A packaged install (see
[Packaged install](#packaged-install-and-auto-update)) sets nothing at compile time
and is configured entirely by `config.json`, so changing the backend URL or the
device token never needs a rebuild.

The config file is a flat JSON object. It is looked up at `PLAYHEADTV_CONFIG` (the
launcher sets it to `<install root>/config.json`), else
`$XDG_CONFIG_HOME/playheadtv/config.json`, else `~/.config/playheadtv/config.json`.
Keys: `playhead_base_url`, `playhead_device_token`, `jellyfin_url`,
`jellyfin_api_key`, `playhead_channel_id`, `working_set_size`,
`epg_sync_interval_minutes`, `start_fullscreen`, `auto_update`, `update_channel` (see
`packaging/linux/config.example.json`). An absent or invalid file is treated as
empty.

| Define | Required | Meaning |
| --- | --- | --- |
| `JELLYFIN_URL` | yes | Base URL of your Jellyfin server, e.g. `https://jellyfin.example.com`. Videos are streamed from `<url>/Videos/<id>/stream?Static=true`. |
| `JELLYFIN_API_KEY` | yes | A Jellyfin API key, sent as the `api_key` query parameter of the stream URL. |
| `PLAYHEADTV_BASE_URL` | yes | Base URL of the Playhead TV backend, e.g. `http://localhost:9394`. |
| `PLAYHEADTV_DEVICE_TOKEN` | in practice | Device API token, sent as `Authorization: Bearer <token>`. Create a device in the backend's admin web UI to get one. If empty no header is sent, and a backend with auth on will reject the requests. |
| `EPG_SYNC_INTERVAL_MINUTES` | no | How often the background syncer refreshes the offline cache (default 15). A failed refresh is retried every 30 seconds until it succeeds. |
| `PLAYHEADTV_CHANNEL_ID` | no | Channel id to start on, only used when the player is opened without an explicit channel. The normal flow (last watched channel, or a mosaic selection) always supplies one, so this currently has little effect. When no channel matches, the player falls back to the channel with the lowest `number`. |

**Start in full screen** (desktop only; Android is always full screen) is on by default.
`PLAYHEADTV_FULLSCREEN` (an environment variable: `1/true/yes/on` or `0/false/no/off`)
or `start_fullscreen` in the config file (a JSON boolean, the variable wins) changes
that default for an installation, so a packaged launcher can start windowed or full
screen. The choice made in Settings is saved and wins over that default.

If any of the three required values is empty the player screen shows an `ERROR:`
message telling you which defines to pass.

The client also persists, in `shared_preferences`: the last watched channel, a short
list of recently watched channels (used by the mosaic), your key binding overrides and
the offline cache settings. The schedule cache itself is a SQLite file in the app support
directory and the poster cache a folder in the app cache directory.

## Run

```sh
fvm flutter pub get
fvm flutter run -d linux \
  --dart-define=JELLYFIN_URL=https://jellyfin.example.com \
  --dart-define=JELLYFIN_API_KEY=<jellyfin-api-key> \
  --dart-define=PLAYHEADTV_BASE_URL=http://localhost:9394 \
  --dart-define=PLAYHEADTV_DEVICE_TOKEN=<device-token>
```

## Packaged install and auto-update

Releases of the client (`client-vX.Y.Z` on GitHub Releases) ship a Linux x86_64
package, `playheadtv-client-X.Y.Z-linux-x64.tar.gz`, with a SHA-256 checksums file.
To install: verify it (`sha256sum -c`), extract it, run `./install.sh`, then edit
`config.json` in the install root (default `~/.local/share/playheadtv`). `install.sh`
also writes a desktop entry; start the app from the menu or with
`<install root>/playheadtv`, the launcher, which is also what a kiosk-style session
should run. Everything lives under that one directory, so no privileges are needed.

On every start the launcher runs a headless updater first: it installs a newer client
release when there is one (checksum verified, swapped in atomically, the previous
version kept), with no prompt, and rolls back a version that did not come up healthy.
It never prevents the installed version from starting: offline, a failed download or
a bad checksum just start what is installed. Set `"auto_update": false` in
`config.json` (or `PLAYHEADTV_AUTO_UPDATE=0`) to disable it; `stable` is the only
release channel. The app also checks the backend's API version against the range it
supports and tells you when it is outside it. Design:
[`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) "Releases and updates".

Releases are signed: the checksums file has a detached Ed25519 signature (the `.sig`
asset), and the updater verifies it against public keys pinned in the app
(`lib/update/pinned_keys.dart`) before installing anything. A release with a missing or
invalid signature is refused and the installed version keeps running; a build with no key
pinned refuses every update (and `tool/package_linux.sh` refuses to package one, see
[`docs/RELEASING.md`](../docs/RELEASING.md) "Signing key").

### AppImage

Each release also carries `playheadtv-client-X.Y.Z-linux-x64.AppImage`, beside the tar.gz
(which stays the default): download it, `chmod +x` it and run it. It needs no `libfuse2`
(the runtime is static; it still needs the kernel's FUSE with a `fusermount3`/`fusermount`
helper, or run it with `--appimage-extract-and-run`). The config is looked up like for any
run without the launcher: `PLAYHEADTV_CONFIG`, else
`${XDG_CONFIG_HOME:-~/.config}/playheadtv/config.json` (created from the example on first
start); the update state and `update.log` are in `${XDG_STATE_HOME:-~/.local/state}/playheadtv`.
The same updater replaces the AppImage file itself (the previous one is kept next to it as
`<name>.previous` and restored if the new one does not come up healthy), so the folder it
lives in must be writable by you for updates to apply. The AppImage was launched and its
update logic is unit-tested, but video playback and hardware decoding from inside the image
have not been validated on a real machine yet (checklist in `docs/RELEASING.md`).

### Update banner and changelog

When a newer release exists the app shows a small banner over whatever is on screen, worded
for how it was installed (the tar.gz or AppImage with auto-update on: it installs on the next
start; auto-update off: how to apply it; a system package, a sandbox such as Flatpak, or a
build from source: the matching instruction). It never interrupts playback or takes focus:
**Select** dismisses it (that version is not announced again; a newer one is), **Back**
closes it (it returns on the next start), and it leaves by itself after a while. After an
update the app shows the changelog of the new version once (the GitHub Release body, as plain
text); Up/Down scroll it if it is long, Select or Back close it. An unreachable GitHub or a
repository without releases shows nothing.

To build the package yourself: `tool/package_linux.sh X.Y.Z`, then
`tool/package_appimage.sh X.Y.Z` for the AppImage (see
[`docs/RELEASING.md`](../docs/RELEASING.md)); `tool/test_launcher.sh` tests the
launcher and installer scripts.

## Controls

Defaults (`defaultKeyBindings` in `lib/input/input_action.dart`); all of them can be
rebound from **Settings > Remap controls** (mosaic Settings tile), with a "Restore
defaults" button.

| Action | Default key |
| --- | --- |
| Next channel / previous channel | Arrow Up / Arrow Down |
| Jump to channel number | `0`-`9` (commits after ~1.5 s, or immediately at 3 digits) |
| Select | Enter: shows the OSD; pressed again with the OSD visible, opens the EPG |
| Back | Esc: closes the EPG, otherwise returns to the mosaic |
| Home | Home: returns to the mosaic |
| Debug overlay | `D` |

Inside the EPG, the channel-next/previous keys move between channel rows, Arrow
Left/Right move through time, Enter on a live program tunes it, and holding Enter
(over 500 ms) expands the program details. The mosaic uses the raw arrow keys and
Enter. Arrow Up/Down toggles between the featured video and the surrounding tiles,
Left/Right walks the tiles. Grid navigation is deliberately not remappable.

## Project structure

```
lib/
  main.dart                     app entry, AppConfig, channel player (preload + crossfade), OSD
  playhead_api.dart             HTTP client for the backend (channels, EPG, catalog items)
  cache/                        offline cache: SQLite schedule cache, background syncer, poster cache
  input/input_action.dart       InputAction enum, default key bindings, KeyBindingsService
  pages/                        mosaic home, settings, "Remap controls", "Offline cache"
  services/settings_service.dart   shared_preferences persistence
  services/app_window.dart      window seam (AppWindow) and the full screen policy
  services/perf_stats.dart      FPS/jank, process CPU/memory and video freeze detection
  widgets/epg_overlay.dart      full-screen EPG (an overlay inside the player page)
  widgets/debug_overlay.dart    debug overlay
test/                           unit/integration test of the API client (real backend)
integration_test/               end-to-end suites and their fixtures (support/)
linux/                          Linux desktop runner
```

## Testing

```sh
fvm flutter analyze
fvm flutter test                                   # needs Go: starts a real backend
fvm flutter test integration_test/<file>.dart -d linux
```

`fvm flutter test` (`test/playhead_api_test.dart`) runs `go run ./cmd/server` from
`../backend` on a free port with a temporary SQLite database.

The suites in `integration_test/` run the real app on the Linux desktop. Each suite
starts its own isolated backend (temporary database, free port) and a small fixture
HTTP server that plays the role of Jellyfin, so no real Jellyfin is needed, and
populates the data through the real REST API (`integration_test/support/`). Key
events are sent through Flutter's test binding rather than the OS. Never point them
at a backend you actually use. The fixture video is generated with `ffmpeg`, found
via the `FFMPEG_PATH` environment variable or, failing that, `ffmpeg` on your `PATH`.

`shared_preferences` is the real, file-backed store during these runs (it cannot be
mocked with `integration_test -d linux`), so each suite clears or seeds it in `setUp`.

## Gotchas

Details are in [`AGENTS.md`](../AGENTS.md); the short version:

- The Linux runner deliberately disables Impeller (it corrupts the `fvp` video
  texture under real playback). Do not re-enable it without re-testing playback.
- Never call `setState` on the player page for video-adjacent UI state (OSD, EPG,
  debug overlay) while the two crossfade controllers exist. Use a `ValueNotifier` with
  a `ValueListenableBuilder` instead.
