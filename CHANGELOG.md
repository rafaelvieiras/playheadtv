# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The backend and the client are released separately: a backend tag is `vX.Y.Z`
and a client tag is `client-vX.Y.Z`. Releases before 0.3.15 (backend) and
0.4.0 (client) are recorded on the [releases page][releases] only.

## [Unreleased]

## [0.5.0] - 2026-11-10 (client)

The phone release: the same app, with a second layout family that a phone and a
television each pick at boot.

### Added

- Phone home: a column of every channel instead of the television's twelve-cell
  ring, with the featured preview capped at 40% of the height.
- Touch as a layer over the keys: tap shows or hides the OSD, a horizontal fling
  switches channel, a swipe up opens the guide, and the guide's axis pans under
  a finger.
- A guide sized for a phone (90 minutes in a 72 px channel column) next to the
  television's three hours, and finger-sized controls everywhere a tap can land.
- Immersive system bars in the player, with every surface reading
  `viewPadding` so a cutout or a gesture handle cannot eat the clock.

### Changed

- The first-run screens (server address, pairing, setup) had no phone layout at
  all: their actions now take the content width with a 48 dp floor, instead of
  being sized by their own label.

### Fixed

- A focus that a window resize dropped is brought back on screen
  (`FocusReveal`), so an arrow key cannot leave the focus somewhere invisible.

## [0.4.0] - 2026-11-09 (client)

- The app boots like a web app's preload: the offline runtime opens before the
  first frame, the first channel's plugin resolve starts before `runApp`, and
  the update check moved to the background after the app is up.
- The updater is two halves, and only the rollback half is on the boot path.
- The launcher supervises the app and restarts a process killed by a signal,
  with a message, instead of it vanishing.

## [0.3.15] - 2026-11-03 (backend)

- Plugin host: install, verify, run, JSON-RPC, with a play route that answers
  `no-store` and never pipes video.
- Declared plugin settings: a plugin names its own keys, and the host refuses a
  key it did not declare or a value of the wrong type.
- Channel logos, served from the `channel` row and readable with a device token.

[releases]: https://github.com/rafaelvieiras/playheadtv/releases
