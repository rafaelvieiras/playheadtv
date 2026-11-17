# Contributing to Playhead TV

Thanks for taking the time. This file covers how to build, test and propose a
change. The full working guide is [`AGENTS.md`](AGENTS.md): architecture, the
client's structure, the offline cache, the logging rules and the conventions
the code already follows. Read the part you are about to touch.

## Ground rules

- **English only.** Code, comments, identifiers, UI strings, log messages, test
  names, docs, commit messages, issues and PRs. The one exception is test data
  that deliberately exercises non-English input, and it carries a comment
  saying so.
- **No personal infrastructure.** No real names or e-mail addresses (inside the
  files; git authorship is yours), no home paths, hostnames, IPs, ports, or
  "on my machine" anecdotes. Use placeholders (`example.com`, `<path>`) and
  environment variables.
- **Commits are human-owned.** No AI or tool co-authorship trailer, no
  "generated with" footer.
- **Start big changes as an issue.** The database schema, the scheduling model
  and the client's video seam are load-bearing; a large PR that changes them
  before anyone agreed on the shape is likely to be declined.

## Prerequisites

| Part | Needs |
| --- | --- |
| `backend/` | Go 1.26 or newer |
| `webui/` | Node.js 20 or newer (CI uses 22) and npm |
| `client/` | Flutter through [`fvm`](https://fvm.app) (the version comes from `client/.fvmrc`), plus the Linux desktop build dependencies |
| integration suites | `ffmpeg` on the machine that generates the fixture clips |
| Docker path | Docker with Compose, for `docker compose up` |

## Build and test

Backend:

```sh
cd backend
go build ./... && go vet ./... && go test ./...
```

A change to a handler or to storage is not done until the real binary has
answered the endpoint: `PLAYHEADTV_DB_PATH=<temp file> PLAYHEADTV_ADDR=:<port>
go run ./cmd/server`.

Web UI:

```sh
cd webui
npm ci
npm run build      # also type-checks
npx playwright test
```

Client:

```sh
cd client
fvm flutter analyze
fvm flutter test                                   # unit and widget tests
fvm flutter test integration_test/<suite>.dart -d linux
client/tool/run_gui_suites.sh                      # desktop suites, headless
```

An integration suite drives a real app against a backend and a fixture video
server of its own. Never point one at an instance whose data you care about:
the suites create channels, blocks and schedules, and some of them stop and
restart the backend on purpose. `AGENTS.md`, "Operational practices", has the
isolation recipe.

## What a good pull request looks like

- One slice: a fix, a feature or a doc change. Not three.
- The test that would have caught the bug, or that pins the new behavior.
- Green gates for what you touched: `go build ./... && go vet ./... && go test
  ./...`; for `client/` also `fvm flutter analyze` and `fvm flutter test`; a UI
  change needs the real pass described above, not only a successful build.
- A short **why** in the description. The diff says what changed; it does not
  say what was wrong.

## What this project does not accept

- **Site-specific source code.** A source that needs a URL pattern, an
  extractor, or a site's name in the polling path belongs in an external plugin
  ([`docs/PLUGINS.md`](docs/PLUGINS.md)), not here. That is a legal and
  store-publication boundary, not a style preference.
- **Server-side transcoding.** The backend never decodes or processes video;
  it owns scheduling metadata and hands out URLs.
- **Bundled binaries nobody can rebuild.** A plugin artifact is built from a
  tagged source by CI, and the host verifies its hash before running it.

## License

By contributing you agree that your work is licensed under the MIT license
([`LICENSE`](LICENSE)).
