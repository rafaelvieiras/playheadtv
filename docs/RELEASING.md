# Releasing

Two independent release streams share this repository. Design:
[`ARCHITECTURE.md`](ARCHITECTURE.md) "Releases and updates".

| | Backend | Client |
|---|---|---|
| Tag | `vX.Y.Z` | `client-vX.Y.Z` |
| Workflow | `.github/workflows/backend-release.yml` | `.github/workflows/client-release.yml` |
| Output | `ghcr.io/<owner>/<repo>-backend:vX.Y.Z` and `:sha-<commit>` (amd64 + arm64), GitHub Release | on a GitHub Release: `playheadtv-client-X.Y.Z-linux-x64.tar.gz`, `playheadtv-client-X.Y.Z-linux-x64.AppImage`, `playheadtv-client-X.Y.Z-checksums.txt` and its signature `...-checksums.txt.sig` |
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

## Signing key

Client releases are signed: the workflow signs the checksums file (which lists
the SHA-256 of the tar.gz and the AppImage) with an Ed25519 key, and the client
verifies that signature against public keys pinned in
`client/lib/update/pinned_keys.dart` before it installs anything (a missing or
invalid signature is a refusal). Design: `ARCHITECTURE.md` "Signed releases".

**Until the real public key is pinned, the client pins none, and the client
release workflow and `package_linux.sh` refuse to run: a client built without a
key refuses every update, it never skips verification.** Do this once before the
first client release:

1. Generate the key pair on a trusted machine (OpenSSL 3):

   ```
   openssl genpkey -algorithm ed25519 -out playheadtv-client-signing.pem
   ```

2. Store the **private** key as the repository secret `CLIENT_SIGNING_KEY`
   (Settings, Secrets and variables, Actions): the whole PEM text, including the
   `BEGIN`/`END` lines. Keep one offline backup (a password manager); never
   commit it, never paste it in an issue.
3. Print the **public** key in the form the client pins (base64 of the raw 32
   bytes) and put it in the list in `client/lib/update/pinned_keys.dart`:

   ```
   openssl pkey -in playheadtv-client-signing.pem -pubout -outform DER | tail -c 32 | base64 -w0
   ```

4. Commit the pinned public key. The release workflow derives the public key from
   the secret and fails when it is not in that list, so the two cannot drift.

The workflow signs with `openssl pkeyutl -sign -rawin` and writes the signature
as base64 text; a downloaded release can be checked by hand with
`base64 -d <checksums>.sig > sig.bin` and
`openssl pkeyutl -verify -rawin -pubin -inkey <public key PEM> -in <checksums> -sigfile sig.bin`.

### Key rotation

The client accepts a **list** of pinned keys (any one verifying is enough), so a
new key never strands installed clients:

1. Generate the new key pair (steps 1 and 3 above). Add the new public key to
   `pinned_keys.dart` next to the old one; leave `CLIENT_SIGNING_KEY` as the OLD
   key. Release this version: it is signed with the old key, which every
   installed client trusts, and it teaches them the new one.
2. Once clients have updated (give it time: an offline client only learns the
   new key when it next updates), replace the secret `CLIENT_SIGNING_KEY` with
   the new private key. The workflow keeps passing because the new key is
   pinned. Releases are now signed with the new key.
3. In a later release, remove the old public key from `pinned_keys.dart`. A client
   that skipped every release in between can no longer verify new ones: it must be
   reinstalled from the release page.

If the private key leaks, rotate immediately as above, but expect that a client
that has not yet seen the new key trusts the leaked one until it updates.

## Client release

1. Make sure `main` is green in CI. Run the GUI suites that cover what changed
   (`fvm flutter test integration_test/<file>.dart -d linux`, one at a time).
   CI validates the desktop build; for the Android build run the lab
   (`client/tool/run_android_suites.sh --changed`, see AGENTS.md "Android
   validation") and put what changed on the device only when it needs hardware.
2. If the client now needs a newer backend API, or was verified against a newer
   one, update `minSupportedApiVersion`/`maxSupportedApiVersion` in
   `client/lib/update/compat.dart`. The release notes quote the range.
3. Tag and push: `git tag client-vX.Y.Z && git push origin client-vX.Y.Z`.
4. The workflow runs the unit tests, builds with
   `client/tool/package_linux.sh` and `client/tool/package_appimage.sh`, signs the
   checksums file (see "Signing key"), verifies the package and attaches the
   archive, the AppImage, the checksums file and its signature to a new release.
   The release body is generated by `client/tool/release_notes.sh`: the commits
   that touched `client/` or `shared/` since the previous `client-v*` tag, then
   the install text. It is also what an updated client shows once as its
   changelog (plain text), so write client commit subjects as you would want to
   read them there. Installed clients pick the release up on their next start; a
   version is only offered when the package and the checksums file of its own
   version are on the release (and an AppImage client needs the AppImage), so do
   not rename them. A release without the `.sig` asset is refused by every
   client.

A release client logs errors only (`PLAYHEADTV_LOG_LEVEL` is not passed to the
build; a build with a release version defaults to `error`). Users attach the log
bundle from Settings -> Diagnostics to bug reports (`playheadtv.log` in the install
root, or in `${XDG_STATE_HOME:-~/.local/state}/playheadtv` for the AppImage); see
`docs/ARCHITECTURE.md` "Client logging".

To build the package locally (the same scripts):
`client/tool/package_linux.sh X.Y.Z [output dir]` then
`client/tool/package_appimage.sh X.Y.Z [output dir]` (the second reuses the first's
build and appends the AppImage to the checksums file; needs the Linux desktop build
dependencies of Flutter, plus `squashfs-tools` for the AppImage; `SKIP_BUILD=1`
re-packages an existing build). A local build with no key pinned is refused unless
you set `ALLOW_UNSIGNED_DEV=1` (an updater that installs unsigned releases:
for trying the updater on a scratch server only, never to be released), or pin
a key at build time with `UPDATE_PUBLIC_KEYS=<base64 key>`.

The AppImage runtime is pinned (version and SHA-256) in `package_appimage.sh`;
to move to a newer type2 runtime release, change `RUNTIME_VERSION` and
`RUNTIME_SHA256` together after checking the new release.

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
- [ ] `playheadtv.log` appears next to `update.log` and stays empty or near-empty
      during normal viewing (the release default is `error`); Settings ->
      Diagnostics creates the bundle with the six buttons.
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

Signed releases and the AppImage (none of this has been run against real
GitHub or real hardware yet):

- [ ] Signing key generated and pinned as described in "Signing key"; the
      secret `CLIENT_SIGNING_KEY` is set; no private key was ever committed.
- [ ] After the first signed `client-v*` tag: the release has the tar.gz, the
      AppImage, the checksums file and the `.sig`; `sha256sum -c` passes on
      downloaded copies; the manual `openssl pkeyutl -verify` command above
      verifies the signature with the pinned public key.
- [ ] Signed release end to end: an installed client (tar.gz) updates to the
      next signed release; then, on a scratch repository, a release with the
      `.sig` removed, one with the checksums file edited after signing and one
      signed with another key are each refused (see `update.log`) and the
      installed version keeps running.
- [ ] Key rotation rehearsal on a scratch repository: a release carrying both
      public keys signed with the old key is accepted, then one signed with the
      new key.
- [ ] AppImage, on a clean Linux x86_64 machine WITHOUT `libfuse2`: download,
      `chmod +x`, run. It starts, plays video, and hardware decoding is active
      (debug overlay); fvp/libmdk and GL work from inside the mounted image.
- [ ] AppImage with the 6-button baseline only (Up, Down, Left, Right, Select,
      Back): the whole flow, including dismissing the update banner with Select.
- [ ] AppImage update: publish a newer signed release; the next start swaps the
      file (a `.previous` copy appears next to it), starts the new version and
      keeps running; the old file's session did not break.
- [ ] AppImage rollback: a release whose app exits at launch is rolled back on
      the next start (the previous file is restored, `state.json` lists the bad
      version under `failed`).
- [ ] AppImage in a read-only folder: the update is skipped with a log line and
      the app starts.
- [ ] A machine without FUSE at all: `--appimage-extract-and-run` starts it.
- [ ] After an update the changelog appears once (plain text, markup shown
      literally, Up/Down scroll if long, Select or Back close it) and does not
      come back on the next start; it matches the GitHub Release body.
- [ ] The new-version banner on a real install of each kind (tar.gz with
      auto-update on and off, AppImage, a build from source): the wording
      matches, Select dismisses it for that version only, a newer release shows
      it again, and an unreachable network shows nothing.
