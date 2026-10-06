# Playhead TV 📺

A self-hosted, multiplatform simulated linear TV app for your own media library.

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

- **Client** (`client/`): Flutter, desktop-first (Linux/Windows), then Android +
  Android TV, then iOS/tvOS. Video engine: [`fvp`](https://github.com/wang-bin/fvp)
  (libmdk), behind a playback abstraction so the engine can be swapped per platform
  later.
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
- **Flutter client** (`client/`, Linux desktop so far): real playback from
  the materialized EPG with preload+crossfade, a mosaic home screen, a
  full-screen EPG overlay and remappable controls.

Not done yet: Android/Android TV builds, Plex/local-folder media sources,
the Cloudflare D1 storage backend for the cloud-sync tier. See
[`docs/ROADMAP.md`](docs/ROADMAP.md) for the full checklist.

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

## License

MIT — see [`LICENSE`](LICENSE).
