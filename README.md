<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="assets/logo.svg">
    <img alt="Playhead TV" src="assets/logo.svg" width="330">
  </picture>
</p>

<p align="center">
  Your own linear TV channels, assembled on the client.<br>
  No server-side transcoding, no tuner.
</p>

<p align="center">
  <a href="https://github.com/rafaelvieiras/playheadtv/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/rafaelvieiras/playheadtv/ci.yml?branch=main&label=CI"></a>
  <a href="https://github.com/rafaelvieiras/playheadtv/releases?q=client-v"><img alt="Client release" src="https://img.shields.io/github/v/release/rafaelvieiras/playheadtv?filter=client-v*&label=client"></a>
  <a href="https://github.com/rafaelvieiras/playheadtv/releases?q=v"><img alt="Backend release" src="https://img.shields.io/github/v/release/rafaelvieiras/playheadtv?filter=v*&label=backend"></a>
  <a href="https://github.com/rafaelvieiras/playheadtv/pkgs/container/playheadtv-backend"><img alt="Image" src="https://img.shields.io/badge/ghcr.io-playheadtv--backend-blue?logo=docker&logoColor=white"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/github/license/rafaelvieiras/playheadtv"></a>
  <img alt="Go" src="https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white">
  <img alt="Flutter" src="https://img.shields.io/badge/dart-3.13%2B-0175C2?logo=dart&logoColor=white">
</p>

Playhead TV exists because the apps that already do this — NostalgiaTV, QuasiTV —
are Android TV/Fire TV only, closed source, and assemble the channel stream on a
**client** with mixed reliability (reviews report broken commercial breaks, episodes
starting mid-way). The server-side alternative — ErsatzTV, Tunarr — solves the
problem the other way: they run ffmpeg on the server to package one continuous
stream per channel, which is heavy (channel surfing across many viewers means many
concurrent ffmpeg pipelines) and requires the client to "tune in" like a TV
tuner instead of getting a real app experience.

Playhead TV keeps the assembly work **entirely on the client**: each client talks
directly to Jellyfin, resolves the day's schedule from the backend, and plays the
original files itself (direct-play when possible), stitching programs, commercials
and bumpers together with its own preload+crossfade logic. The backend never touches
video — it only owns scheduling metadata and user/device sync.

- **Client** (`client/`): Flutter, on Linux/Windows desktop, Android TV and
  Android phones, with iOS/tvOS still ahead. One codebase, two layout families
  (a television and a phone each pick theirs at boot). Video engine:
  [`fvp`](https://github.com/wang-bin/fvp) (libmdk) behind a playback
  abstraction, plus an opt-in ExoPlayer engine on Android for decoders that
  reject libmdk's surface.
- **Backend** (`backend/`): Go. Owns channels, program blocks, filler/commercial
  rules (pre-roll/mid-roll/post-roll/tail), users, devices, and media source catalog
  sync (Jellyfin in v1; the schema is source-agnostic — see
  [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)). Exposes a REST API and an MCP
  server (`modelcontextprotocol/go-sdk`) so an LLM agent can manage channels and
  schedules directly; together with the web UI that's the only authoring surface —
  playback clients don't build schedules, they just resolve and play them.
- **Shared** (`shared/`): API contracts consumed by both sides.
- **Docs** (`docs/`): [`ARCHITECTURE.md`](docs/ARCHITECTURE.md) (backend design) and
  [`ROADMAP.md`](docs/ROADMAP.md) (actionable checklist) for now, source for the
  future documentation site later.

## Status

Working today:

- **Backend** (Go, SQLite for the self-hosted tier): REST API for channels,
  media sources, catalog, blocks, filler pools/rules and schedule entries;
  an EPG materializer that expands schedules into a rolling window (48h by default, `PLAYHEADTV_EPG_WINDOW`)
  (`GET /epg`, `GET /epg/meta`); user sessions and device API tokens;
  credentials encrypted at rest; Jellyfin pull sync (plus push sync for
  sources the backend can't reach); dynamic, criteria-based blocks and
  filler pools.
- **MCP server**: everything the web UI can do, as MCP tools at `/mcp` in the
  same binary (one tool per REST operation; destructive tools need
  `confirm=true`; a test fails when a route has no tool).
- **Admin web UI** (`webui/`): React SPA embedded in the Go binary and
  served under `/admin/` — EPG grid, channels, blocks, schedule, filler,
  media sources and devices.
- **Flutter client** (`client/`): Linux and Windows desktop, Android TV and
  Android phones. Real playback from the materialized EPG with
  preload+crossfade, a mosaic home screen (twelve-cell ring on a television, a
  channel column on a phone), a full-screen EPG overlay, remappable controls, a
  channel OSD with an always-on clock, channel logos, an offline cache that
  keeps the guide readable with the backend down, and an updater that verifies
  signed release checksums.
- **Plugins** (`docs/PLUGINS.md`): a source that needs an extractor is an
  external plugin that runs on the backend, never in the client and never as
  site-specific code here. A plugin declares its own settings, and the client
  draws the settings screen from that declaration. The first one, the
  [Playhead TV plugin for YouTube](https://github.com/rafaelvieiras/playheadtv-plugin-youtube),
  is its own repository, MIT like this one.

Not done yet: Plex and local-folder media sources, iOS and tvOS builds, the
Cloudflare D1 storage backend for the cloud-sync tier, and a real multi-user
permission model (every deployment is one tenant today). See
[`docs/ROADMAP.md`](docs/ROADMAP.md) for the full checklist and
[`CHANGELOG.md`](CHANGELOG.md) for what shipped recently.

### Run with Docker

```sh
docker compose up -d
curl http://localhost:9394/healthz
```

Persists its SQLite file in a named volume (`playheadtv-data`), so data
survives restarts.

### Production deployment and releases

A production deployment (a Compose project pinned to a published image tag,
synced by a GitOps manager) is described in [`deploy/`](deploy/README.md). Releases
are cut from tags: `vX.Y.Z` publishes the backend image to GitHub Container
Registry, `client-vX.Y.Z` publishes the packaged Linux client, which updates itself
from those releases. See [`docs/RELEASING.md`](docs/RELEASING.md). The running
backend reports its version at `GET /version`.

### Logging

The backend logs as it always has. Set `PLAYHEADTV_LOG_LEVEL` (`error`, `warn`, `info`,
`debug` or `trace`) to filter it by level; unset keeps today's output. The client has its
own log file and Settings -> Diagnostics screen to attach to a bug report; see
[`client/README.md`](client/README.md#logging).

### Encryption key

Media source credentials (e.g. a Jellyfin API key) are stored encrypted at
rest (AES-256-GCM). On first run, if no key is configured, the backend
generates one and saves it next to the database file as `encryption.key`
(0600 permissions) — the log makes this loud and explicit. **Back up that
file together with your database**: losing it means losing the ability to
decrypt any stored credential (nothing else in the database is affected).

To supply your own key instead (e.g. for a reproducible deployment), set
`PLAYHEADTV_ENCRYPTION_KEY` to 32 random bytes, base64-encoded:

```sh
export PLAYHEADTV_ENCRYPTION_KEY=$(openssl rand -base64 32)
```

## Development

Build, test and run commands for the backend, web UI and client, plus the
project's working conventions, are in [`AGENTS.md`](AGENTS.md). The Flutter
client has its own notes in [`client/README.md`](client/README.md).

## Contributing

Contributions are welcome. [`CONTRIBUTING.md`](CONTRIBUTING.md) has the
prerequisites, the commands to run before opening a pull request, and the rules
this repository does not bend (English only, no personal infrastructure in the
files, no AI co-authorship, no site-specific source code). Security problems go
through [`SECURITY.md`](SECURITY.md) - please do not open a public issue for
them.

## License

MIT — see [`LICENSE`](LICENSE).
