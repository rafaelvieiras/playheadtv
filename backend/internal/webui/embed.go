// Package webui embeds the built admin SPA (webui/, built with
// `npm run build`, writes directly into dist/ — see webui/vite.config.ts)
// so the Go binary can serve it without any external file dependency.
//
// dist/ only has index.html tracked in git (a placeholder until someone runs
// the real build); the rest of dist/ is gitignored. That keeps `go build`
// and `go test` working on a fresh clone or in CI with no Node involved,
// while a real `npm run build` (locally or in the Docker build stage)
// overwrites the placeholder with the real compiled assets.
package webui

import "embed"

//go:embed all:dist
var FS embed.FS
