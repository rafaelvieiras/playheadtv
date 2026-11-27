# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The backend and the client are released separately: a backend tag is `vX.Y.Z`
and a client tag is `client-vX.Y.Z`. Releases before 0.3.15 (backend) and
0.4.0 (client) are recorded on the [releases page][releases] only.

## [Unreleased]

## [0.9.0] - 2026-11-27 (client)

### Added

- A channel change is announced by a compact banner in a corner (channel logo,
  number, program, synopsis and its span) instead of the full-width OSD, and the
  change itself waits a configurable delay (3 s by default) with the previous
  channel still playing, so rapid surf costs the media server nothing and only
  the channel the viewer stops on is opened. Settings gained `Channel switch
  delay`, `Channel banner time` and `Channel banner corner`.
- Holding Select opens the guide directly; a short press still opens the OSD.
- The guide's channel column leads with the logo, with the number small above
  the name and the channel on air highlighted, and opening the guide centres the
  channel on air instead of leaving the list at the top.
- The guide's details panel shows the item's backdrop, the series and channel
  logos, the year, the age rating, both codecs, the length, the community score
  and the synopsis (all of it served by `/epg`, no per-entry request).
- The OSD's progress bar shows how much of the program is buffered ahead of the
  playhead.

### Fixed

- The OSD is usable while a channel loads: it describes the program from the
  guide, so Select over the spinner opens the real OSD instead of a bare
  "number · name" line.
- A change armed by the delay no longer re-arms itself when it starts loading,
  which left the banner on the right channel and the picture on the previous one.
  The OSD also follows the announcement instead of showing the channel left
  behind.
- Closing the track picker closes the OSD it was opened from and gives the arrow
  keys back to the player.
- A video engine that never finishes opening is abandoned after 20 s
  (`channelOpenBound`) and the load moves on, instead of leaving the channel on a
  spinner for ever.

## [0.5.4] - 2026-11-27 (backend)

### Added

- `GET /epg` carries what a guide needs to describe a program without a second
  request: `overview`, `content_rating`, `community_rating`, `production_year`,
  `genres`, `audio_codec`, `audio_channels` and `bitrate`, next to the video
  shape it already sent. The audio columns are new (migration `0030`) and are
  filled by the pull sync from the media file's first audio stream; a row synced
  before this release keeps nulls until its source is synced again. Additive:
  `APIVersion` does not move and a client that ignores the fields keeps working.

## [0.5.3] - 2026-11-25 (backend)

### Added

- `GET /epg` carries `video_codec`, `container`, `width` and `height` for every
  entry and every tail clip. All four are optional: an older backend sends none,
  and "no answer" means "try to play it". They come from the catalog row the
  materializer already references, so no query was added, and the change is
  additive: `APIVersion` does not move and a client that ignores the fields keeps
  working.

## [0.8.3] - 2026-11-25 (client)

### Fixed

- A program this device cannot decode is no longer played, and no longer
  retried. The client asks the device before a player is built
  (`CodecProbe.kt`, a `MediaCodecList` lookup, so no decoder instance is
  created), and a decoder that refuses on open counts as the same answer. The
  channel then says so - what cannot play, why, and when it is back - and waits
  for the next program instead of playing anything, because retrying was what
  turned one 320x240 MPEG-4 AVI into 49 players in fifteen minutes, a full Java
  heap and a process aborted by the runtime. The preload stops after a bounded
  number of attempts, and the picture watchdog's reopen budget is cleared only
  by playback that is really advancing, not by a controller that merely opened
  (client).
- Leveling lifts quiet material instead of only cutting loud material. The lift
  was a fixed pre-amp and a player volume stops at 1, so nothing quieter than
  `target - pre-amp` could ever reach the target: measured over 13 consecutive
  transitions, 7 reported 0.0 dB of gain and the quietest item played 11.6 dB
  under the target, which is why the app needed a higher television volume than
  everything else. The lift now follows the quietest item on air (capped at +15
  dB), each player keeps the attenuation that is left over, and the device
  remembers the measurement (`LUFS|peak`) rather than a volume. The default
  target moves to -14 LUFS, where the streaming services sit. Measuring a short
  item got cheaper too: the window is half of what is left of the item, and the
  probe decodes at 4x real time (client).

## [0.8.2] - 2026-11-24 (client)

### Fixed

- Leveling no longer throws on a platform that has no pre-amp and no
  measurement. The platform end of the loudness feature (the `LoudnessEnhancer`
  on the output mix, and the probe) exists only on Android, and every other
  platform answers the `playheadtv/loudness` channel with a
  `MissingPluginException`, which no `on PlatformException` arm catches. The
  pre-amp is applied at boot and the probe runs for every item, so a desktop
  build raised an uncaught async error on its first frame and then once per
  item. Nothing audible changed - the gain stays unavailable where there is no
  pre-amp - and the log line it wrote claimed the platform had *refused* the
  pre-amp, which sends anyone reading it to the wrong problem. That warning now
  fires only when a platform answers while holding no control over the output
  mix; a desktop is silent.

## [0.8.1] - 2026-11-23 (client)

### Fixed

- The dimmer now survives a restart. The level was saved and the on/off toggle
  was not, on the theory that a dimmed picture at boot reads as a fault, so
  every update - and any other restart - brought the picture back at full
  brightness and the viewer had to find the button again. Both are saved now
  (`dim_level_percent`, `dim_active`): the app opens as the viewer left it, and
  an installation nobody dimmed still starts undimmed.

## [0.8.0] - 2026-11-22 (client)

### Added

- An on-screen log toggle in the OSD, next to the captions, audio and dimmer
  buttons. A build that carries the log (`--dart-define=PLAYHEADTV_LOG_PEEK=true`)
  draws its last lines over the picture; until now the only way to turn them off
  was to rebuild without the define. The new button hides and shows the lines at
  runtime and the choice is remembered, so the app can be used normally while it
  keeps logging — hiding the overlay never stops the log.
- The channel logo feature reaches the guide and the admin UI: the guide's
  channel column draws `number [logo] name` with a fixed 92x44 logo slot (a
  square mark and a wide one take the same room, so the names line up), and the
  channel's Deco tab uploads, replaces and removes the logo. `docs/ARCHITECTURE.md`
  "Channel logo".

## [0.7.3] - 2026-11-22 (client)

### Fixed

- A transition no longer puts a dead player on air. A preloaded controller
  waits off screen for the preload lead - or for a whole schedule gap - and an
  idle connection can be closed under it meanwhile; the engine reports that only
  when the item is asked to play, which is the transition itself. The client
  promoted it anyway, logged the item on air and dropped the outgoing
  controller, leaving the channel black with nothing left to ask: the preload
  retry loop only covers the preparation phase, and the stall watch needs a
  controller whose position is frozen. The incoming item is now started before
  the fade begins and promoted only once it is reading its source
  (`lib/playback_promotion.dart`); a failure reopens the same unit and tries
  again, and after that resumes it with a hard cut. The fade never starts on an
  item that has not shown a picture, the item on screen keeps the screen while
  this happens, and no unit is ever skipped.

## [0.7.2] - 2026-11-22 (client)

### Fixed

- A channel that claims to be playing while it has no live picture is now
  reopened instead of showing black until somebody restarts the app. Seen after
  a transition whose incoming controller was built and took a video surface but
  never played, while the outgoing one had already been disposed: no position
  existed to observe, so the picture-stall watch could not see it, and the
  preload of the next unit kept retrying with nothing on screen. The new watch
  covers the missing-controller case (`PictureLostWatcher`,
  `lib/playback_stall.dart`); it reopens the same channel and never skips a
  unit, because a skipped unit is invisible to the viewer and the guide would
  disagree with what is on screen. Two reopenings, then an error on screen.

## [0.7.1] - 2026-11-21 (client)

### Fixed

- A channel change no longer inherits the level of the item that was on air. A
  quiet item (volume near 1.0) handing over to a loud one played the loud one at
  the quiet one's volume for the seconds the measurement and the fade took, which
  on the music channels was heard as "it was much louder for a moment". The new
  channel now starts from its own prior; with no prior either, it starts at the
  level of the loudest material the library holds, so the correction can only go
  up (a short swell instead of a burst).
- A correction that raises the volume now ramps in 2 s instead of 4 s: it starts
  from an estimate, and an estimate should not be heard for long.

## [0.7.0] - 2026-11-21 (client)

Leveling that makes the library louder instead of quieter. The 0.6.0 leveling
could only attenuate (`Player.setVolume` stops at 1), so the whole library came
down to its quietest content and the result was reported as too quiet.

### Added

- A global pre-amp: `LoudnessEnhancer` on the output mix (session 0), which is
  the only gain above unity available without patching the video player plugin.
  Measured on API 28 and API 36: it applies the requested gain exactly and has a
  limiter, so peaks pushed past full scale compress instead of clipping. When the
  platform refuses it, leveling degrades to attenuation only and the channels
  still play.
- Settings rows for leveling on/off, the target (-16, -18, -20, -22 LUFS) and the
  pre-amp (0, +6, +9, +12, +15 dB). The pre-amp applies immediately; the target
  clears the remembered gains, since they were computed against the old pair.
- `loudness_preamp_db` in the config file.

### Changed

- The default target moved from -24 to -18 LUFS. Over a measured sample of 47
  items that lifts the library 4.1 dB on average, makes 36 of 47 items louder,
  and takes the final spread from 28 dB to 1.3 dB.
- The gain is now `min(10^((target - LUFS - preamp)/20), 10^((-1 - peak - preamp)/20))`:
  the pre-amp counts toward both the average level and the peak ceiling.

### Fixed

- A different audio track no longer reuses the previous track's gain. One film in
  the library carries a 2.0 dub at -6.2 LUFS next to a 5.1 track at -26.9 LUFS,
  so the wrong pair of these is a 20 dB error.
- An item whose two windows disagree by more than 6 dB is left unleveled instead
  of remembered with a guess: two windows of one item differ by 2.6 dB typically,
  but a malformed file measured 14 dB apart at two offsets.


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
