# Releasing

Two independent release streams share this repository. Design:
[`ARCHITECTURE.md`](ARCHITECTURE.md) "Releases and updates".

| | Backend | Client |
|---|---|---|
| Tag | `vX.Y.Z` | `client-vX.Y.Z` |
| Workflow | `.github/workflows/backend-release.yml` | `.github/workflows/client-release.yml` |
| Output | `ghcr.io/<owner>/<repo>-backend:vX.Y.Z` and `:sha-<commit>` (amd64 + arm64), GitHub Release | `playheadtv-client-X.Y.Z-linux-x64.tar.gz` + `playheadtv-client-X.Y.Z-checksums.txt` on a GitHub Release |
| Consumed by | the operator's GitOps repository (`deploy/README.md`) | installed clients (headless updater) and first installs |

Pushes and pull requests run `.github/workflows/ci.yml` (backend build, vet,
test; web UI build and tests; client analyze, unit tests and the launcher
script tests). The Flutter GUI integration suites are not part of CI: run the
relevant ones by hand before a client release.

## Backend release

1. Make sure `main` is green in CI and the working tree is what you want to
   ship.
2. If the REST contract changed in a way an older client cannot cope with
   (removed or reshaped field/endpoint), bump `APIVersion` in
   `backend/internal/buildinfo/buildinfo.go` and, once a client is verified
   against it, `maxSupportedApiVersion` in `client/lib/update/compat.dart`.
   Additive changes do not bump it.
3. Note in the release description whether the release carries a database
   migration (`git diff <previous tag> -- backend/migrations`). Operators take a
   backup before such an upgrade.
4. Tag and push: `git tag vX.Y.Z && git push origin vX.Y.Z`.
5. The workflow gates on build/vet/test, pushes the image and creates the
   release. Check the package page shows `vX.Y.Z` and `sha-<commit>` for both
   platforms.
6. Operators upgrade by bumping the tag in their deployment repository.

## Client release

1. Make sure `main` is green in CI. Run the GUI suites that cover what changed
   (`fvm flutter test integration_test/<file>.dart -d linux`, one at a time).
2. If the client now needs a newer backend API, or was verified against a newer
   one, update `minSupportedApiVersion`/`maxSupportedApiVersion` in
   `client/lib/update/compat.dart`. The release notes quote the range.
3. Tag and push: `git tag client-vX.Y.Z && git push origin client-vX.Y.Z`.
4. The workflow runs the unit tests, builds with
   `client/tool/package_linux.sh`, verifies the package and attaches the archive
   and the checksums file to a new release. Installed clients pick it up on
   their next start; a version is only offered when both assets of its own
   version are on the release, so do not rename them.

To build the package locally (the same script):
`client/tool/package_linux.sh X.Y.Z [output dir]` (needs the Linux desktop build
dependencies of Flutter; `SKIP_BUILD=1` re-packages an existing build).

## Manual validation checklist (not covered by CI)

Pipelines and the packaging script were exercised in isolation; these steps need
real infrastructure and are done once by the maintainer, and again after
changes to the pipeline or the updater.

Backend and GitOps:

- [ ] After the first `v*` tag: the workflow is green, the image has both
      platforms, and `docker run --rm <image> healthcheck` against a running
      container reports healthy. Make the package public (or log in) so the
      manager can pull it.
- [ ] The Compose-aware manager deploys `deploy/compose.example.yml` (with a real
      tag), the container turns healthy and `GET /version` shows the tag.
- [ ] Admin bootstrap with `POST /app-users`, then sign in to `/admin/`.
- [ ] Bump the tag to a newer release through the manager: the new version
      runs, data is intact; on a migration-bearing release the backup and
      restore procedure of `deploy/README.md` works.
- [ ] The hardened options of the example (`cap_drop: ALL`, loopback port, named
      volume) behave with the real image on the target host.

Client on a real Linux install:

- [ ] After the first `client-v*` tag: the release has the archive and the
      checksums file and `sha256sum -c` passes on a downloaded copy.
- [ ] Extract on a clean Linux x86_64 machine, run `./install.sh`, edit
      `config.json`, start from the desktop entry and from the launcher in a
      kiosk-style session.
- [ ] The app plays with a gamepad or remote only (the 6-button baseline: Up,
      Down, Left, Right, Select, Back), Back returns as expected, and hardware
      decoding is active (debug overlay).
- [ ] Publish a newer `client-v*` release: the next start updates with no prompt
      and runs the new version (`update.log` in the install root, `current`
      link).
- [ ] Break an update on purpose (a release whose app exits at launch): after
      the next start the previous version runs, and `state.json` lists the bad
      version under `failed`.
- [ ] Offline start (no network): the installed version starts without delay
      beyond the update timeout and plays from its cache.
- [ ] `auto_update: false` in `config.json` stops the check.
- [ ] Point a client at a backend outside its API range (or temporarily change
      the supported range): the notice appears, Select dismisses it, nothing
      crashes.
- [ ] A checksum mismatch (publish a release with a wrong checksums line on a
      scratch repository) is refused and the installed version keeps running.
