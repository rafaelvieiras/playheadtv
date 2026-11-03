# Plugins

Some content sources cannot live in this repository. Taking video from a
third-party site means carrying an extractor that those sites change
constantly, and that extractor's legal footing is not this project's to
grant. So the core stays generic: it knows how to *install, run and talk to*
a plugin, and nothing about any particular site.

A **plugin** is an out-of-process artifact that provides two things the core
never implements itself:

- **catalog listing** — the videos/items behind one source, for the backend's
  pull sync (the backend owns the grid, so it needs the list);
- **media resolution** — a playable URL for one item, at the moment a client
  is about to play it (URLs from these sources expire, so they can never be
  stored in the guide).

This document is the contract. The Go backend (`backend/internal/pluginhost`)
and the Flutter client (`client/lib/plugin/`) implement the *host* side; a
plugin release implements the *plugin* side. All three follow the same
protocol, so a plugin released for one host works on the other.

Nothing in `backend/` or `client/` may contain site-specific extraction code,
URL patterns or credentials. If a change would put any of that in the core,
it belongs in the plugin instead.

## Artifact

One release per platform, a `tar.gz` (or `.zip` on Windows) with a **flat
layout**:

```
plugin.json          # manifest, see below
<entrypoint>         # executable named by the manifest, mode 0755
...                  # anything else the plugin needs
```

The host extracts the archive into a directory it owns, `chmod +x`es the
entrypoint, and starts it. The archive's SHA-256 is the identity the host
verifies before extracting; the entrypoint is the only file the host
executes.

An artifact may carry whatever it needs — including another program it runs
as a child process. That program is the plugin's business, not the core's,
and it is distributed inside the plugin's own release.

## Manifest (`plugin.json`)

```json
{
  "id": "example",
  "name": "Example source",
  "description": "One line a user can read in Settings.",
  "version": "2026.10.26",
  "protocol": 1,
  "entrypoint": "example-plugin",
  "capabilities": ["catalog.list", "media.resolve"]
}
```

| field | required | meaning |
| --- | --- | --- |
| `id` | yes | Stable, lowercase, `[a-z0-9][a-z0-9-]*`. What a source and the EPG refer to. Never reuse an id for different code. |
| `name` | yes | Shown to the user. |
| `description` | no | One line, shown next to the name. |
| `version` | yes | The plugin's own release version. Free-form, but orderable strings are strongly preferred (`2026.10.26`, `1.4.0`). |
| `protocol` | yes | Host protocol the plugin speaks. **1** is the only version so far. |
| `entrypoint` | yes | Path of the executable inside the artifact, relative to its root. |
| `capabilities` | yes | Subset of `["catalog.list", "media.resolve"]`. A host may drive a plugin with only one of them (the backend only lists; a client only resolves). |
| `config` | no | The settings a source of this plugin takes, so a screen can be built from the plugin's own description. See below. |

### Declared settings (`config`)

A plugin's source configuration is its own: the host stores it as opaque JSON
and hands it back verbatim. That leaves nothing to build a settings screen
from — unless the plugin says what it accepts. `config` is that declaration:

```json
"config": [
  {"key": "max_height", "label": "Quality", "type": "choice",
   "help": "The tallest the stream may be.",
   "options": [{"value": "1080", "label": "1080p"},
               {"value": "720", "label": "720p"}]},
  {"key": "max_items", "label": "Items to list", "type": "number"},
  {"key": "audio_only", "label": "Audio only", "type": "bool"}
]
```

| field | required | meaning |
| --- | --- | --- |
| `key` | yes | The config member the value is written to. `[a-z][a-z0-9_]*`, unique within the list. |
| `label` | yes | Shown next to the setting. |
| `type` | yes | `choice`, `bool`, `number` or `text`. It says how the value is written into the config: a `number` field as a JSON number, `bool` as a boolean, everything else as a string. |
| `options` | for `choice` | The values to offer, in the order to offer them: `{"value": "<written>", "label": "<shown>"}`. |
| `help` | no | One line of plain text under the setting. |

The declaration is a promise about the plugin's own config, and a host may
reject a manifest that cannot be rendered (an unknown `type`, a `choice` with
no options, a key that is not a member name). It says nothing about *values*:
the plugin still validates what it is given at resolve and list time, because a
config can also be written by an older screen or by hand.

## Protocol

Version **1**, over the plugin's **stdin/stdout**:

- one JSON object per line, UTF-8, `\n` terminated;
- **stdout carries protocol traffic only** — anything else corrupts the
  stream;
- stderr is free-form for the plugin's own diagnostics; the host captures it
  and logs it at debug level (it never becomes protocol data);
- the host starts the process, sends `info`, then its requests, and closes
  stdin to ask for a clean exit (`shutdown` is the polite version).

Request: `{"id": <int>, "method": "<name>", "params": {...}}`
Success: `{"id": <int>, "result": {...}}`
Failure: `{"id": <int>, "error": {"code": "<string>", "message": "<string>"}}`

`id`s are chosen by the host, increase monotonically, and must be echoed
verbatim. A plugin must answer requests in any order (the host may pipeline
them), and must not answer an `id` it did not receive.

The host enforces a timeout per call and kills the process when it fires; a
plugin that needs longer must return a cursor and let the host ask again
rather than block a call (`catalog.list` pagination, below).

### `info`

No params. Result: the manifest object. The host uses it to confirm the
plugin id and protocol it installed, and to show the version.

### `catalog.list`

Fetches one page of the source's items — the backend's whole view of the
catalog.

```json
{
  "config": {"url": "https://example.com/some-channel"},
  "cursor": "",
  "limit": 50
}
```

- `config` is the source's stored configuration, verbatim (see *Binding a
  source to a plugin*). Its shape is the plugin's business.
- `cursor` is `""` on the first call and the previous page's
  `next_cursor` after that.
- `limit` is a suggestion; the host accepts any page size.

Result:

```json
{
  "items": [
    {
      "external_id": "abc123",
      "title": "An episode",
      "kind": "episode",
      "duration_ms": 1320000,
      "series_title": "A series",
      "genres": "documentary",
      "production_year": 2026,
      "source_modified_at": 1793000000000
    }
  ],
  "next_cursor": ""
}
```

Fields are the same ones `POST /media-sources/{id}/catalog-sync` accepts
(`backend/internal/api/media_sources.go`'s `catalogSyncItem`), so the push
and pull paths agree on the shape. `external_id`, `title` and `duration_ms`
are required; `duration_ms` **must be > 0** (an item of unknown or zero
length breaks channel assembly — the same rule Jellyfin sync applies to
`RunTimeTicks`). `kind` is `movie`, `episode` or `other`; anything else is
rejected by the host.

`next_cursor` empty means the listing is complete. A host that gets an empty
`items` array with a non-empty `next_cursor` twice in a row gives up, so a
plugin must not return an empty page with a cursor.

### `media.resolve`

Turns one item into something a player can open **now**.

```json
{
  "config": {"url": "https://example.com/some-channel"},
  "item": {"external_id": "abc123", "duration_ms": 1320000},
  "start_at_ms": 0,
  "modes": ["single", "pair"]
}
```

`start_at_ms` (optional) is where in the item playback begins: a channel tunes
into the middle of a program. `modes` (optional) is what the host's player can
play; absent means `["single", "pair"]`.

### Modes: the plugin never makes the player's work heavier than it must

Some sources no longer serve one file that carries both video and audio. The
answer must still be one thing a player can open, so a response declares which
shape it chose:

| mode | result | who does the work |
| --- | --- | --- |
| `single` | `url` | nobody: the player opens the source's own file or manifest |
| `pair` | `sources[]`, one `video` and one `audio` | the **player**, by merging two tracks it already knows how to play |
| `remux` | `url` on the plugin itself | the **plugin**, by rewriting the container |

- **A plugin must never transcode.** No decoding and re-encoding, in any mode.
  These hosts are often a 2 GB set-top box or a single-board computer, and the
  core cannot undo a plugin that spends CPU per second of video.
- `single` and `pair` cost the plugin nothing: it only reports URLs that point
  at the source's own bytes. A host should offer both, and a plugin must prefer
  `single` when the source has it.
- `remux` exists for a host whose engine cannot merge a pair. It is `-c copy`
  (container rewrite, no re-encode) and it must be bounded: a sliding window of
  segments, deleted as it advances, so one playback never holds an unbounded
  amount of media on disk. A plugin must only use it when the host names it in
  `modes`, so no host pays for it by accident.
- A host that cannot play what the plugin can offer gets `unsupported`; it must
  then hide the channel rather than play an error.

Result, `single` or `remux`:

```json
{
  "mode": "single",
  "url": "https://media.example.com/…",
  "mime": "video/mp4",
  "expires_at": 1793003600000,
  "headers": {"Referer": "https://example.com/"}
}
```

Result, `pair`:

```json
{
  "mode": "pair",
  "sources": [
    {"type": "video", "url": "https://media.example.com/v.mp4", "mime": "video/mp4", "expires_at": 1793003600000},
    {"type": "audio", "url": "https://media.example.com/a.m4a", "mime": "audio/mp4", "expires_at": 1793003600000}
  ]
}
```

- `url` must be directly playable (progressive file or an HLS/DASH manifest) by
  the client's engine, and a `pair` must be two tracks of the same instant of
  the same item. Neither may be bytes streamed through the backend: the backend
  never touches video. A `remux` URL is produced by the plugin process, on the
  machine that plays it, and exists only for that playback.
- `expires_at` (Unix ms, optional but strongly recommended) is when the URL
  stops working. The host must not reuse a URL past `expires_at + 60s`, and a
  client must never persist a resolved URL anywhere — the guide stores catalog
  metadata only.
- `headers` are sent verbatim by the player, and may be refreshed on every
  resolve.
- The host calls this **per playback** (a channel switch, a preload, a resync),
  never once per EPG entry.

### `shutdown`

No params, empty result. The host waits briefly for the process to exit,
then kills it. A plugin that ignores it is not broken, just slower to stop.

### Errors

`code` is a short machine-readable string the host may act on; `message` is
for logs. Reserved codes:

| code | meaning |
| --- | --- |
| `config` | The source's `config` is missing or unusable; a human must fix it. |
| `not_found` | The item/source is gone (deleted or private). |
| `unavailable` | Temporary upstream failure; retrying later may work. |
| `rate_limited` | Upstream asked for a slow-down. The host backs off. |
| `unsupported` | The plugin cannot serve this request (wrong capability, protocol too new). |

## Registry

A registry is one JSON document listing plugins and their artifacts. The
plugin's own release process publishes it; the backend and the client both
consume it.

```json
{
  "schema": 1,
  "plugins": [
    {
      "id": "example",
      "name": "Example source",
      "description": "One line.",
      "versions": [
        {
          "version": "2026.10.26",
          "protocol": 1,
          "artifacts": [
            {
              "os": "linux",
              "arch": "amd64",
              "url": "https://example.com/example-linux-amd64.tar.gz",
              "sha256": "9f2c…",
              "size": 4310244
            }
          ]
        }
      ]
    }
  ]
}
```

- `os` is `linux`, `windows`, `darwin` or `android`; `arch` is `amd64`,
  `arm64` or `arm` (the Go naming, so the host never translates).
- `sha256` is lowercase hex over the **archive**, and is mandatory: a host
  refuses an artifact whose hash does not match, and refuses an artifact with
  no hash at all.
- `url` may point anywhere (a release asset, a CDN). Hosts fetch it through
  their own cache when they have one.

### Hosts serve it their own way

The backend is a **caching mirror**, not a publisher:

- `GET /plugins` — the registry document it is configured with
  (`PLAYHEADTV_PLUGIN_REGISTRY_URL`), cached, with artifact `url`s rewritten
  to its own `/plugins/{id}/{version}/{os}-{arch}` endpoints so a client that
  cannot reach the internet still installs (and so every client download is
  one cache fill).
- `GET /plugins/{id}/{version}/{os}-{arch}` — the archive, downloaded once
  and cached on disk, served with `ETag` and an immutable `Cache-Control`.

A backend with no registry configured serves an empty list, and a plugin
source then simply reports "plugin not installed". Nothing else breaks.

### Trust

The archive hash protects integrity in transit and in cache. When the
registry is **signed** (an Ed25519 signature over the registry document,
published next to it), a host configured with the corresponding public key
verifies the signature *before* trusting any hash inside it, and refuses the
whole registry otherwise. The client reuses the key it already pins for
application updates (`client/lib/update/pinned_keys.dart`); the backend takes
its key from `PLAYHEADTV_PLUGIN_PUBLIC_KEY` (base64, or a file path).

Signature verification is optional in the sense that a host with no key
configured accepts an unsigned registry — that is the self-hosted default,
same trust as "the admin typed the URL". It is never optional for a host that
has a key: no key means no protection, a key means nothing unverified runs.

## Binding a source to a plugin

`media_source` gains three columns (migration `0028_plugin.sql`):

- `type = 'plugin'`;
- `plugin_id` — which plugin lists this source;
- `plugin_min_version` — the oldest plugin version that can read this
  `config` (empty means any);
- `plugin_config_json` — the plugin's own configuration, stored verbatim and
  passed back as `config`.

A plugin source is a **pull** source like Jellyfin's: the backend lists it
through the installed plugin on its periodic sweep (`internal/pullsync`), so
the grid materializes without any client being online. `base_url` and
`credential_cipher` are unused by plugin sources; `plugin_config_json` is
plain text and must not hold secrets — a plugin that needs a token takes it
from its own configuration, not from this column.

## Requirements in the guide

The client resolves URLs, so it needs to know which plugins a channel
requires *before* it tries to play it. `GET /epg` carries that per channel:

```json
"requirements": [
  {"channel_id": "…", "plugins": [{"id": "example", "min_version": "2026.10.26"}]}
]
```

It is the union, over the entries in the window, of the plugins behind each
entry's catalog item. A client that has the plugin (and it is enabled) shows
the channel normally. A client without it:

- **does not show** the channel in the mosaic or the guide — the alternative
  is a channel that tunes into an error;
- shows one line in Settings — "N channels are hidden, they need the
  `example` plugin" — with an install action when the client has a registry
  (see below).

Installing a plugin is an explicit user action on the client: it downloads
the artifact, verifies the hash and the signature, stores it, and only then
re-fetches the guide. There is no silent auto-install.

## Android

**A plugin never runs on the device**, so Android needs no transport of its own.
It used to: the first design ran the plugin inside the client, and Android would
have needed the artifact shipped inside an APK (`lib/<abi>/`, since API 29
refuses to execute a downloaded binary) and bound to as a `Service`. Moving the
plugin to the backend removed the requirement, not just the transport: the
extractor, the remuxer and the process do not exist on a TV box at all, and a
device only asks the backend for a URL (`GET /catalog-items/{id}/play`).

What is left of the platform question on a device is the *archive*: nothing on a
client downloads or selects one, so the registry's `artifacts` list is read only
by the backend that installs them. The client's plugin code
(`client/lib/plugin/`) holds the registry types it displays and the channel
filter.

## Who may change what

- **Read, any paired device**: `GET /plugins`, `GET /plugin-sources`.
- **A paired device may write**: the settings a plugin declares for its own
  sources (`PUT /plugin-sources/{id}/settings`), and turning an installed plugin
  on or off or removing it (`PUT`/`DELETE /plugins/{id}`). The device is what
  shows the guide the plugin produced, so it is the first to see that plugin
  break, and neither call brings in new code.
- **Admin session only**: `POST /plugins/{id}/install`, which downloads a
  program from the registry and runs it on the backend, and the artifact
  download that serves it. A client that offers an install would be offering a
  call the backend refuses; `client/lib/pages/plugins_page.dart` says who
  installs instead.
- **The admin web UI** carries the session-only half: *Plugins*
  (`webui/src/pages/PluginsPage.tsx`) installs a version the registry offers,
  updates an installed one, turns it off or on, removes it, and edits the
  settings each plugin declares for its sources — the form is built from that
  declaration, so a plugin that adds a setting needs nothing here.

## What a host must never do

- Run an artifact whose SHA-256 does not match the registry (or whose
  registry signature does not verify when a key is configured).
- Cache a resolved URL, or write one to the guide, the cache or a log.
- Pass the plugin's stdout/stderr through to a user-visible channel without a
  level check.
- Let a plugin failure break a channel that does not need it: a failed plugin
  means *its* sources have no items, not that the materializer stops.
- Ask for `remux` by default. A host lists the modes its engine actually
  supports, and `single`/`pair` come first; the expensive mode is chosen only
  when nothing cheaper can play.
- Assume a `pair` can be played everywhere. A host that cannot merge two tracks
  says so in `modes` instead of silently playing a video track with no audio.
