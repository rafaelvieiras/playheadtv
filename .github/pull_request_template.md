## What this changes

<!-- One paragraph: what, and why. -->

Closes #

## How you verified it

- [ ] `cd backend && go build ./... && go vet ./... && go test ./...`
- [ ] `cd webui && npm ci && npm run build` (and a Playwright pass for a UI change)
- [ ] `cd client && fvm flutter analyze && fvm flutter test`
- [ ] The affected integration suite, against a backend and a fixture media
      server of its own (`fvm flutter test integration_test/<suite>.dart -d linux`)
- [ ] A manual check of the screen or the endpoint this touches

## Notes for the reviewer

<!-- Trade-offs, things you decided against, and anything that is still open. -->
