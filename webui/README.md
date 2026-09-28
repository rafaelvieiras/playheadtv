# Playhead TV admin UI

The web admin/authoring UI for Playhead TV: log in, browse the EPG grid, and
manage channels, blocks, schedule entries, filler pools/rules, media sources
and paired devices. It is the primary authoring surface (alongside the MCP
server); playback clients never edit scheduling.

## Stack

React 19 + TypeScript + Vite, Tailwind CSS v4 with shadcn/ui (Radix)
components, TanStack Query for server state, Zustand for the auth store,
React Router, lucide-react icons and sonner toasts. Linted with `oxlint`.

## Development

```sh
npm install
npm run dev      # Vite dev server
npm run build    # type-check (tsc -b) + production build
npm run lint
```

The app is served under `/admin/` (`base` in `vite.config.ts`), so open the
dev server at `/admin/`. API calls use absolute paths on the same origin
(`/channels`, `/epg`, ...), so run the Go backend behind the same origin
(for example by proxying, or by using the production build served by the
backend) when developing against real data.

## How it is built and served

`npm run build` writes straight into `backend/internal/webui/dist`, which the
Go binary embeds with `go:embed` (`backend/internal/webui`) and serves at
`/admin/`. There is no separate Node server in production, and because the
UI and API share an origin, no CORS setup is needed. `backend/Dockerfile`
runs this build as its first stage, so `docker compose build` always ships a
fresh UI. `webui/` and `backend/` must stay sibling directories for that
output path to resolve.

## Talking to the backend

All requests go through `src/lib/http.ts`, a thin `fetch` wrapper that
attaches the session token (`Authorization: Bearer ...`, obtained from
`POST /sessions` and kept in `src/store/auth.ts`). A `401` response clears the
session and redirects to the login page. Shared API types live in
`src/lib/types.ts` and mirror `backend/internal/domain` and the request shapes
in `backend/internal/api`.

## Layout

- `src/pages/` — one page per resource (EPG, channels, blocks, schedule,
  filler pools/rules, media sources, devices, login)
- `src/components/` — dialogs, layout and shadcn/ui primitives
- `src/lib/`, `src/store/` — HTTP client, types, auth store
