<!-- A design analysis, not a decision log: the binding rules live in
     ARCHITECTURE.md and AGENTS.md. Slice 1 is implemented; slices 2-8 are not.
     This file is the working plan for the phone UI and can be updated as the
     slices land. -->

I have read the binding docs and the code named in the task. Here is the plan.

---

# Phone UI for the Playhead TV Flutter client — analysis and implementation plan

Scope: analysis + staged plan only. Nothing below has been built. No file was modified, no build or test was run.

Verdict up front: **the phone is not a smaller TV.** Three of the six screens survive a phone unchanged once safe-area insets and touch targets are handled (Settings, Plugins + its dialogs, pairing/update/dimmer overlays); the OSD, the guide and the mosaic are laid out in *proportions* that assume a 16:9 1080p surface read from 3 m, and on a 360–430 dp handheld those proportions produce a 60 %-of-screen OSD, a 24 dp-wide guide cell and a mosaic frame occupying 26 % of the screen. The fix is a small number of named seams (a per-process form factor, plus per-screen pure metrics objects), not per-page `if (isPhone)`.

---

## 1. What "phone" means here: landscape for the picture, portrait-safe for everything else

Measured from the constants in the code (all numbers are design px scaled by the UiScale that `resolveUiScale` would return for that window today, i.e. `shortestSide/1080` clamped to `[0.6, 3.0]` — a 360 dp phone lands on the 0.6 floor):

| Surface | Portrait 360×780 | Landscape 780×360 | TV 1920×1080 |
|---|---|---|---|
| Mosaic frame (`mosaic_layout.dart`: 664×384 units, fills height) | scale 0.52 → frame **345×200 dp** (26 % of the height), cells **83×47 dp** | scale 0.90 → frame 597×345, cells 144×81 | scale 2.5 → 1660×960 |
| EPG time viewport (width − 2·16k − 220k) at the 3 h span | **146 dp / 180 min = 0.81 dp/min**; a 30-min cell = **24 dp** | 566 dp → 3.1 dp/min; 30-min cell = 94 dp | 1668 dp → 9.3 dp/min; 30-min cell = 278 dp |
| OSD bottom panel (≈256 design px without the channel strip, ≈347 with it) | 154–208 dp of 780 (20–27 %) | **218–295 dp of 360 (60–82 %)** | 256–347 of 1080 (24–32 %) |
| EPG corner thumbnail (320×180 design px) | 192×108 dp = **53 % of the width** | 192×108 = 25 % | 800×450 |

Three conclusions, in order of how much they constrain the plan:

1. **The picture and the guide want landscape.** A 16:9 stream in a 360 dp-wide portrait viewport is 360×203 dp: 74 % of the screen is black bars. The guide's channel column is a fixed 220 design px; on a 360 dp-wide screen at 0.6 that column alone is 132 dp = 37 % of the width, leaving 0.81 dp/min — a half-hour program becomes a 24 dp sliver, narrower than any touch target and too narrow for a title.
2. **The OSD's problem is height, not width.** In landscape at 0.6 the panel is 218 dp tall on a 360 dp screen. Its height comes from rows that a phone does not need: a 5-button transport row that is `enabled: false` in every build (play/pause, prev, rewind, ff, next), and the "Channels" card strip (12 + 19 + 8 + 52 design px). Dropping both and reflowing gives ≈110 design px.
3. **Portrait must not be blocked, only de-prioritised.** A user rotates mid-episode. Every screen has to hold at 360 dp short side; only the *primary* target is landscape.

**Recommendation.** Landscape-primary for the player and the guide. The mosaic becomes a **vertical list on a phone in both orientations** (a 16:9 featured card capped at 40 % of the viewport height, then one row per channel), which makes it aspect-agnostic instead of wasting 74 % of a portrait screen. Settings / Plugins / pairing / offline cache / diagnostics / remap are already `ListView`-based and are portrait-natural. **Do not lock orientation** (`SystemChrome.setPreferredOrientations`): a lock is a per-Activity call, it fights the user, and if a layout is unusable in portrait the bug is in the metrics, not in the orientation. `android:configChanges` already lists `orientation|screenSize|density`, so a rotation does not recreate the Activity — the player keeps playing and only layout boxes change, which is the same property `mosaic_nav_scale_test` already exercises at 1280×720 → 2560×1080.

Trade-off, stated plainly: landscape costs a rotation on a device that is usually held portrait, and the mosaic loses its signature "satellite waiting screen" ring. In exchange the guide stays navigable and the OSD stops covering the picture.

---

## 2. Input model: touch is additive, never a replacement

The 6-button baseline in `AGENTS.md` is not "the TV rule" — it is "every user-facing flow must be fully operable with exactly six inputs". A phone is a *superset* device: it has touch, and it may also have a paired remote/controller. So the model is:

- **Keys: unchanged, all of them.** Touch never removes a key path. A phone with a paired D-pad or a Bluetooth gamepad must work exactly like the TV box (aliases already exist: `selectKeyAliases` includes `gameButtonA`, `backKeyAliases` includes `goBack` and `gameButtonB`). Nothing in the 6-button model changes; the phone inherits it for free because it runs the same widgets.
- **Touch: an additional input path to the same outcomes.** Every `InputAction` gets a touch affordance *or* a documented accelerator exemption (the channel-digit case, which `AGENTS.md` already establishes, still holds on a phone because the mosaic list and the guide both reach "any channel" with taps).
- **No third model.** Do not invent a "touch mode" that changes what Select means; touch calls the same callbacks the focus path calls (`activateFocused`'s target functions).

### Action → touch map

| `InputAction` | Key path (unchanged) | Phone touch affordance | Notes |
|---|---|---|---|
| `channelNext` / `channelPrevious` | raw Up/Down in the player | horizontal fling on the picture; any row in the mosaic list; any live cell in the guide | fling, not double-tap (see §4) |
| `select` | Enter / DPAD_CENTER / gamepad A | tap. On the picture: show/hide the OSD. On an OSD control: activate it. On an EPG cell: focus+tune if live. On a list row: open | needs `onTap` added to every OSD control and every guide/list row — see §3 |
| `back` | Esc / `goBack` | Android system Back **and** the back gesture (arrives as `goBack`, see §8); every dialog also has a tappable dismiss | `BackKeyGuard` already guarantees the platform default cannot skip the exit dialog |
| `home` | Home | none needed (same outcome as `back`) | stays an accelerator |
| `digit0`–`digit9` | typing a channel number | none | declared accelerator; the 6-button/touch equivalent (mosaic list, guide) exists and is the path that must be exercised in testing |
| `debugOverlay` | D | none | diagnostics stay a desktop/TV tool; a phone reaches the log through Settings → Diagnostics (already a 6-button/touch path) |
| `trackSelector` | no default binding (accelerator) | the OSD's CC / audio buttons | |
| `cycleAudioTrack` / `cycleSubtitleTrack` | unbound accelerators | — | |
| dimmer + level | OSD dimmer button in focus mode; hold Select = level dialog | tap the dimmer button; **long press** = level dialog (the `GestureDetector` already exists on that one button) | |

### Must remain reachable with touch alone

Watch a channel; switch channel; see what is on and until when; open the guide; pick any channel from the guide; pick a subtitle or audio track; dim the picture and change the level; open Settings and every page under it; pair the device (soft keyboard); read/save a plugin source setting (soft keyboard); dismiss the update banner/changelog; leave the app (Back at the mosaic → exit dialog → Exit).

Two of those are **dead today** and are hard blockers, not polish:

- `TrackSelectorOverlay` rows are plain `Container`s inside a `_FocusSelector` with no `onTap` (`lib/widgets/track_selector_overlay.dart`, `_buildPanel`): a phone user who taps the OSD's CC button opens a picker they cannot operate.
- `DimmerLevelDialog` is keyboard-only (`Focus(onKeyEvent: _onKey)`, no pointer handler): the level chip and the dimmer long-press lead to a dead screen.
- `UpdateOverlay`'s banner and changelog have no pointer handler and deliberately take no focus: on a phone they are unclosable. Fix without breaking "never takes focus": wrap plain `Text`s in `GestureDetector`s (a `GestureDetector` has no `FocusNode`, so it stays out of traversal and the 6-button path via Select is unaffected).
- `EpgOverlay` cells have no `onTap`: the guide is unauthorisable by touch.

---

## 3. Screen by screen

Legend: **change** = phone-specific work; **must not change** = contract to preserve.

| Screen | Changes on a phone | Must not change |
|---|---|---|
| **Mosaic** (`pages/mosaic_page.dart`, `widgets/mosaic_frame.dart`) | phone renders a **vertical list** (`PhoneMosaicView`): featured channel as a 16:9 card capped at `0.4 × height`, then one row per channel (number, logo/name, "on now" title from the cache), then a Settings row. All channels, sorted by number, recent first, no 12-tile cap (`_resolveTiles` currently caps at `mosaicRingSlots.length - 1`). Tap selects; D-pad Up/Down still walks the list. Safe-area insets. | The preview is still one decorative `VideoPlayback`, still `mosaicPluginPreviewDwell` before a plugin resolve, still torn down on `hidden`/`paused`, still never holds the wake lock. `mosaic_test`, `mosaic_nav_scale_test` on the TV profile must stay green byte-for-byte. |
| **Player + OSD** (`main.dart`, `widgets/osd_overlay.dart`) | phone OSD = compact bottom sheet from `OsdMetrics`: no dead transport row, "Channels" strip replaced by a **Guide** button and a **Channels** button, 3 real buttons (CC, audio, dimmer) at ≥48 dp, one-line title, progress bar, "Ends HH:MM". Tap on each control (not focus). A tap on the picture toggles the OSD without entering focus mode; focus mode still exists for a paired remote. Safe-area insets on the panel and the clock. | The player page never calls `setState`. `_osdVisibleNotifier` / `_osdArmed` / `_osdFocusNotifier` / `_epgOpenNotifier` / `_watermarkNotifier` stay ValueNotifiers, and the video widgets stay `child:` of the `ValueListenableBuilder`s. The crossfade, preload, gap slate, `_playbackOffset`, `StreamCache`, stall watcher and lifecycle teardown are untouched. `playerBackgroundColor` stays a static `Scaffold` background. |
| **EPG** (`widgets/epg_overlay.dart`, `epg_guide.dart`) | `GuideMetrics.phone`: visible span **90 min**, channel column **72 design px** (number + optional 24 px logo, no name), row height and header unchanged; thumbnail setting defaults **off** on a phone (still a setting, `EpgThumbnailSetting`). Tap a cell to focus it and tune if live; horizontal drag scrolls the time axis (writes the same `_hOffsetMinutes`); pinch changes the span (90 / 180 / 360 min) through the same metrics value; a Close button. Still an overlay in the player's `Stack`, still `_epgOpenNotifier`. | `buildGuideCells` and the tail-cap rules: identical inputs, identical attribution as the OSD. Focus/scroll/clock stay notifiers, never `setState` per key press. The corner thumbnail is still the *same* `VideoPlayback` moved by `AnimatedPositioned`. Read only from `ScheduleCache`, never the backend. |
| **Settings** (`pages/settings_page.dart`) | none structurally: it is `ListView` + `ListTile` + `UiScaled`, i.e. already touch-first. Needs: `UiScaled` must not shrink text to 0.6 on a phone (see §6), rows ≥48 dp (Material's `ListTile` already is), safe-area insets, and the `Interface size` row keeps working with phone bounds. | Same rows, same `Key`s, same Back handling via `Focus(canRequestFocus: false, skipTraversal: true)`. No new authoring row. |
| **Plugins + source settings dialog** | tap already works (`ListTile.onTap`, `TextField`). Needs: dialog `maxHeight: context.ui(640)` at 0.85 is 544 dp — on a 360 dp-tall landscape phone that overflows; switch to `maxHeight: min(ui(640), 0.9 × height)`. Keep one `FocusNode` per row and keep the row focusable while saving (the two hard-won rules in `AGENTS.md`). | The write path (`PUT /plugin-sources/{id}/settings`), the "no install here" rule, and the manifest-driven rendering. The phone must not gain `POST /plugins/{id}/install` or any `/media-sources` route. |
| **Pairing / setup / server URL** | the only real gain on a phone: the soft keyboard. `PairingPage`'s QR + PIN and `SetupPage`/`ServerUrlPage` `TextField`s already work; only insets and keyboard avoidance need checking (`adjustResize` is set). | The PIN/poll flow, `DeviceIdentity.detect()` as the only source of `isTv`, no secret in logs. |
| **Update banner / changelog** (`widgets/update_overlay.dart`) | `GestureDetector` on the banner ("Do not show this version again") and on the changelog's Close text; the changelog body already scrolls by drag. Hint text becomes input-neutral ("Tap or Select"). | Never takes focus, never rebuilds a page, still `Positioned` above the Navigator, still plain text from an untrusted release body. |
| **Dimmer + clock** (`widgets/dimmer_overlay.dart`, `widgets/clock_overlay.dart`) | insets only: the always-on clock's `top: ui(20), right: ui(32)` can land under a cutout — add `MediaQuery.paddingOf` to both. The dimmer stays pointer-transparent, which is what lets player gestures work while dimmed. | Driven by `Dimmer.active` / `Dimmer.level` / `ClockOverlaySettings.*` ValueNotifiers, `Positioned.fill`/`Positioned` in every branch (the bare-Stack-child GL bug), `IgnorePointer`, never focusable. |
| **Exit dialog** | already tappable (`TextButton` with `autofocus`), insets only. | Cancel keeps the focus; the app still exits only through `AppExitController`. |

---

## 4. Gestures: a minimal set, each checked against the TV semantics

Accepted:

1. **Tap on the picture** → show the OSD (restart the 5 s countdown); tap again → hide it. *Conflict check:* on TV the same two presses are the armed-OSD toggle; touch skips the "armed" intermediate state because there is no ambiguity to resolve (nothing else is bound to a direction). It does not break anything: `_osdArmed` stays whatever it was, and the first D-pad arrow still enters focus mode.
2. **Horizontal fling on the picture** → `channelNext` / `channelPrevious`. *Conflict check:* no pointer path exists today; the channel-switch coalescing (`_channelLoadInFlight` / `_pendingChannelSwitchTarget`) already absorbs repeated triggers, which is exactly the fling-mashing case. A fling is safe against accidental taps: a tap has zero velocity.
3. **Swipe up on the picture** → open the guide (same as Select, Select / the OSD's Guide button). *Conflict check:* none on TV. Two-finger-free, and it does not start at a screen edge.
4. **Horizontal drag inside the guide** → move `_hOffsetMinutes` (the same animated-minutes offset the arrows drive). *Conflict check:* the vertical `ListView` owns vertical drags; a horizontal drag recognizer inside the row `Stack` wins only on horizontal displacement, so row scrolling still works.
5. **Pinch inside the guide** → change the visible span between 90 / 180 / 360 min. *Conflict check:* no zoom exists anywhere else in the app, and it is a *read-only* change of the metrics value, not of the schedule.
6. **Tap on a guide cell** → focus it; if its displayed span covers now, tune. *Conflict check:* a drag does not fire `onTap`, so scrolling never tunes.
7. **Long press on the OSD dimmer button** → level dialog. Already implemented; the only thing missing is a touch path *out* of that dialog (§2).

Rejected, with the reason:

- **Double-tap to skip / seek.** The transport row is dead (`enabled: false`) and there is no seek: a channel is linear TV, and `_playbackOffset`/`_resyncAfterRunningBehind` already decide where the picture belongs. A skip gesture would contradict "the schedule is where you are". It also collides with tap-to-toggle-OSD.
- **Double-tap left/right to switch channel.** Two taps already mean show+hide OSD; using them for a channel change makes an accidental switch one slip away.
- **Swipe down on the picture.** Nothing coherent to bind: the OSD is not a modal sheet, and "down = dimmer" is undiscoverable.
- **Edge swipes from the bottom or either side.** Reserved by Android's gesture navigation; the OS wins, and the app must not depend on a gesture the platform may steal.
- **Pinch/zoom on the player.** The video is letterboxed by design (`BoxFit.contain`, chosen so embedded subtitles are not cropped); zooming would crop exactly what that decision protects.
- **Pull-to-refresh anywhere.** The syncer is the *only* component allowed to ask the backend for schedule data. A user-visible refresh gesture would be a second writer of the offline-cache contract. The touch path is Settings → Offline cache → Sync now.
- **Drag on the mosaic to change focus.** Tap selects in a list; a drag-to-focus model adds nothing.

---

## 5. TV and phone in one codebase: signals and seams

### Which signal decides

**Platform first, size only to break the tie on a mobile OS.** Concretely:

- Android with `UiModeManager` reporting `UI_MODE_TYPE_TELEVISION` → **tv**. The client already asks this exactly once, at boot, through the `playheadtv/device` channel (`MainActivity.kt` → `DeviceIdentity.detect()`); Dart cannot tell an Android TV from a phone any other way, and `DeviceIdentity.platform()` already depends on it.
- Any non-mobile platform (Linux, Windows) → **tv**. This is what keeps every existing desktop test, golden and integration suite meaningful, including the ones that deliberately resize the app to 600×1000, 800×450 or 320×180 — those must keep the 10-foot layout, not silently become "phone".
- Android/iOS without the TV flag → **phone** when `shortestSide < 600`; else tv (a tablet is a big screen; treating it as 10-foot is a defensible, harmless call, and iOS is not a target yet).

Input device is **not** part of the signal: a phone with a gamepad is still a phone, a TV with a USB mouse is still a TV. Touch support is unconditional and additive, so no branch is needed.

**The form factor is resolved once, at boot, and never re-derived from a window resize.** That is deliberate: it removes the "one rebuild above the Navigator" hazard from the GL-texture rules and it stops a resized desktop window from changing personality. Responsiveness *within* a form factor is the metrics' job.

### The seams (signature level)

```dart
// lib/ui/form_factor.dart  (new, pure except for the InheritedWidget)
enum ClientFormFactor { tv, phone }

/// dp; a 720p TV box has shortestSide 720 and must stay tv, so the guard is
/// only ever consulted on a mobile platform that is not reporting television.
const double phoneMaxShortestSide = 600;

ClientFormFactor resolveFormFactor({
  required Size size,
  required bool isTelevision,
  required bool isMobilePlatform,
});

class ClientFormFactorScope extends InheritedWidget {
  static ClientFormFactor of(BuildContext context);
}

/// Resolved once at boot by `_InitialRouteResolverState._resolve` (which already
/// awaits SharedPreferences) and read by the host; never re-resolved on resize.
class ClientFormFactorState {
  static final ValueNotifier<ClientFormFactor> notifier;   // defaults to tv
}

extension ClientFormFactorContext on BuildContext {
  ClientFormFactor get formFactor;
  bool get isPhone;
}
```

```dart
// lib/ui/ui_scale.dart  (signature changes; every default reproduces today)
const double phoneUiScaleReferenceSide = 420;   // 360 dp → 0.86, 430 dp → 1.02
const double uiScaleFitHeightPhone = 200;       // the compact OSD panel
const double uiScaleFitWidthPhone = 360;

double autoUiScale(Size size, {ClientFormFactor formFactor = ClientFormFactor.tv});
double maxFitUiScale(Size size, {ClientFormFactor formFactor = ClientFormFactor.tv});
double resolveUiScale(Size size, {double? override, ClientFormFactor formFactor = ClientFormFactor.tv});

/// A design size with a dp floor, for anything a finger must hit.
double uiTouch(double px);          // extension on BuildContext: max(ui(px), kMinInteractiveDimension)
```

```dart
// lib/ui/osd_metrics.dart  (new, pure)
class OsdMetrics {
  final EdgeInsets panelPadding;    // tv: fromLTRB(32,48,32,20)
  final double logoSize, titleSize, metaSize, chipSize, iconSize, progressHeight;
  final bool transportRow;          // tv: true (5 dimmed buttons), phone: false
  final bool recentChannelCards;    // tv: true, phone: false (Guide/Channels buttons instead)
  final bool endsAt;                // both true
  static OsdMetrics of(ClientFormFactor f);
  // OsdMetrics.of(tv) must equal today's literals exactly — a golden proves it.
}
```

```dart
// lib/ui/guide_metrics.dart  (new, pure)
class GuideMetrics {
  final Duration visibleSpan;        // tv: 3h, phone: 90min (user-adjustable 90/180/360)
  final double channelColumnWidth;   // tv: 220, phone: 72
  final double rowHeight;            // 84 both
  final double headerHeight;         // 44 both
  static GuideMetrics of(ClientFormFactor f);
}
```

```dart
// lib/mosaic_nav.dart  (one new entry point; the 2-D algorithm is unchanged)
enum MosaicLayoutMode { ring, list }

int mosaicMove({
  required MosaicLayoutMode mode,
  required int tileCount,
  required int focused,
  required MosaicDirection direction,
  int? returnTo,        // ring only
});
// ring -> today's mosaicNavigate (unchanged); list -> clamp(focused ± 1) on up/down,
// no-op on left/right.
```

```dart
// lib/mosaic_tiles.dart  (new, extracted from _MosaicPageState, pure)
class MosaicTile { final PlayheadChannel? channel; final bool isSettings; }

({List<MosaicTile> tiles, int featured}) buildMosaicTiles({
  required List<PlayheadChannel> channels,
  required List<String> recentIds,
  required MosaicLayoutMode mode,
  Random? random,     // injected so the shuffle is testable
});

// lib/widgets/phone_mosaic_view.dart  (new)
class PhoneMosaicView extends StatelessWidget {
  final List<MosaicTileSpec> tiles;
  final int focusedIndex;
  final Widget featuredBackground;
  final void Function(int index) onSelect;
}
```

```dart
// lib/widgets/player_gestures.dart  (new)
class PlayerGestureLayer extends StatelessWidget {
  const PlayerGestureLayer({
    required this.onTapPicture,
    required this.onHorizontalFling,   // -1 = previous, +1 = next
    required this.onSwipeUp,
    this.enabled = true,               // false while the guide or a picker is open
  });
}
```

```dart
// lib/services/system_bars.dart  (new; same shape as screen_awake.dart / app_window.dart)
abstract class SystemBars { Future<void> setImmersive(bool on); }
@visibleForTesting SystemBars systemBarsOverride = const SystemChromeBars();
class SystemBarsController {   // edge-triggered, never throws, never throws from build
  SystemBarsController([SystemBars? platform]);
  Future<void> playerOpened();   // immersiveSticky, no bars
  Future<void> playerClosed();   // edgeToEdge
}
```

Where they sit: `PlayheadSpikeApp.builder` becomes
`ClientFormFactorHost(child: UiScaleHost(child: BackKeyGuard(child: UpdateOverlayHost(child: ...))))` — the form-factor host must be **above** `UiScaleHost` because `resolveUiScale` takes the factor as an argument. Every page below sees `context.formFactor` for free, which is what keeps `if (isPhone)` out of page code: the pages pick a *metrics object* or a *view widget*, once.

Total per-page branching after this: `MosaicPage.build` picks one view widget; `EpgOverlayState` reads one metrics object; `ChannelOsd.build` reads one metrics object; `MosaicPage._move` calls `mosaicMove`. Nothing else.

---

## 6. UiScale and typography on 360–430 dp

Today `resolveUiScale` returns `shortestSide/1080` clamped to `[0.6, 3.0]`. On every phone that is the 0.6 floor, and that single multiplier is wrong in both directions at once:

| What breaks | Why | Fix |
|---|---|---|
| OSD height | Panel is 256–347 design px because of a dead transport row and the channel strip; at 0.6 that is 154–208 dp — 60 %+ of a landscape phone | `OsdMetrics.phone`: drop `transportRow` and `recentChannelCards`, tighter `panelPadding`, ≈110 design px |
| Touch targets | `_button` is `padding 8 + icon 28` = 44 design px → **26 dp** at 0.6, **37 dp** at 0.85. `kMinInteractiveDimension` is 48 | `context.uiTouch(px)` = `max(context.ui(px), 48)` on every tappable control; the phone control row is a `Row` of `uiTouch(48)` boxes |
| Chip text | `_chip` is 11 design px → **6.6 dp** at 0.6 (illegible) | `OsdMetrics.phone.chipSize = 14` → 12 dp at 0.86; a widget test walks the tree and asserts no resolved font size < 12 |
| `UiScaled` pages | `MediaQuery.textScaler = TextScaler.linear(0.6)` shrinks Material body text to ~9.6 sp while `ListTile` keeps its 48/68 dp height | phone scale range `[0.85, 1.3]` (reference 420 dp) instead of `[0.6, 3.0]`; `UiScaled` then multiplies by ~0.86, i.e. stock Material sizes, which is what a phone wants |
| Guide cell width | Fixed 220 design channel column + a fixed 3 h span at 0.6 → 24 dp cells | `GuideMetrics.phone`: 72 design channel column, 90 min span → 90 dp cells in portrait, 230 dp in landscape |
| Guide thumbnail | 320×180 design px = 53 % of a portrait phone's width, and it *reserves a hole* in the grid's background bands | default off on a phone (`EpgThumbnailSetting` default by form factor; still a setting) |
| `maxFitUiScale` | Its design budget (`440×640`) encodes "the OSD at scale 1 on 1080p"; on a phone a *fixed* 1.0 choice is capped to `min(360/440, 780/640) = 0.818` — **larger** than the automatic 0.6, i.e. the "cap" makes it worse | per-form-factor fit constants (`200×360`) and a phone automatic range; `resolveUiScale` keeps its shape, so `Settings → Interface size` still works and is still capped |

The rule to keep: **design sizes stay written as `ui(px)` — the mechanism does not change.** What changes is *which constants* a screen multiplies, and that touch targets get a dp floor applied after the multiply. `osdPosition`, the EPG's ppm-from-width derivation, the `UiScaleHost` placement above the Navigator and the "never `setState` on the player page" rule are all untouched.

---

## 7. Tests: what is pure, what needs pixels

**Pure Dart / widget tests (fast, no device):**

| Test file | Proves |
|---|---|
| `test/form_factor_test.dart` (new) | `resolveFormFactor`: 360×780 & 780×360 on a mobile OS → phone; TV flag at any size → tv; Linux at 640×360 / 800×450 / 320×180 → **tv** (the existing suites' assumption); 430×932 → phone; 600 boundary |
| `test/ui_scale_test.dart` (extend) | phone automatic factor in `[0.85, 1.3]` at 360–430 dp; `maxFitUiScale` per form factor; `uiTouch` floor; every tv default byte-identical to today |
| `test/osd_metrics_test.dart` (new) | `OsdMetrics.of(tv)` equals today's literals; the phone panel fits 360 dp height at scale 1.0 and every tappable is ≥48 dp |
| `test/guide_metrics_test.dart` (new) | at 360 dp and 780 dp wide viewports, a 30-min cell is ≥48 dp and ≥64 dp respectively; tv metrics unchanged |
| `test/mosaic_tiles_test.dart` (new) | ring mode caps at 12 + featured with a deterministic `Random`; list mode returns every channel, recent first, Settings last, featured index 0 |
| `test/mosaic_nav_test.dart` (extend) | `mosaicMove` list mode clamps at both ends and ignores left/right; ring mode results identical to today's `mosaicNavigate` cases |
| `test/player_gestures_test.dart` (new) | tap fires `onTapPicture`; a horizontal fling fires `onHorizontalFling` with the right sign; a vertical drag fires `onSwipeUp` and **not** the horizontal callback; a drag beginning inside the OSD panel never reaches the layer; `enabled: false` swallows everything |
| `test/phone_typography_test.dart` (new) | walk `find.byType(Text)` on the phone OSD/guide/settings at 360×780 and 780×360: no resolved font size below 12, no overflow exception |
| `test/epg_guide_test.dart`, `test/osd_subject_test.dart` | unchanged — the attribution rules must not move |

**Goldens.** Yes, the phone goldens belong next to `test/ui_scale*_golden_test.dart`, in the same `client/test/` directory and the same `goldens/` folder, in a **new file** `test/phone_layout_golden_test.dart` so the TV goldens stay a frozen baseline that nobody regenerates by accident. Sizes (devicePixelRatio 1.0, text is Ahem so these check layout and proportions, not glyph shapes):

- `360×780` and `430×932` portrait,
- `780×360` and `932×430` landscape,
- each of the above also with `tester.view.padding = FakeViewPadding(top: 24, bottom: 24, …)` for one "cutout" case, so the safe-area insets are pinned.

Screens per size: mosaic list (with and without a featured preview), phone OSD (closed-focus and with the dimmer chip), guide (90-min span, thumbnail off, and once with a thumbnail), Settings, Plugins, the plugin settings dialog, the exit dialog, the update banner, the track picker with tap affordances. `goldens/` gets `phone_*` names so `--update-goldens` on one file cannot touch the other's images.

**Device (Android lab).** New suites, run with `client/tool/run_android_suites.sh -p phone`:

- `integration_test/phone_touch_test.dart` — real boot on the phone profile: tap shows the OSD, fling left switches channel, swipe up opens the guide, tap on a live cell tunes, tap on the picture hides the OSD, system Back from the player goes to the mosaic, Back at the mosaic opens the exit dialog.
- `integration_test/phone_layout_test.dart` — walks mosaic → player → guide → Settings → Plugins → pairing at the phone geometry, asserts no overflow exception and that the OSD panel, clock and guide header sit inside the safe area.
- `integration_test/phone_overlay_touch_test.dart` — track picker option tap applies a track; dimmer long press opens the level dialog and a tap saves a level; the update banner is dismissible by tap.

`pref_boot_epg_test.dart` gets a phone run for the guide's opening frame (the metric change alters pixels-per-minute and the number of laid-out cells; the existing "titles fill in over frames" mechanism should be re-measured, not assumed).

**Suite bookkeeping that the plan must not skip.** `client/tool/run_android_suites.sh` has one suite list plus `DESKTOP_ONLY`; there is no notion of "this suite asserts the 10-foot layout". On `-p phone` these will legitimately fail and must be listed as TV-only (a `TV_ONLY` set beside `DESKTOP_ONLY`): `mosaic_nav_scale_test` (asserts `frameHeight == 720*0.96` and 21:9/portrait ring geometry), `epg_layout_test` (asserts ≥6 half-hour ticks = the 3 h span), `settings_navigation_visual_test` (already desktop-only, resizes at will). `client/tool/suite_map.tsv` needs new lines: `client/lib/ui/*` → `BOOT phone_layout_test phone_touch_test mosaic_test epg_layout_test osd_test`, and the new widget files → their own suites.

---

## 8. Android specifics a phone makes matter

- **Cutouts and system bars.** The app draws edge-to-edge-ish already but never reads insets. Add: `edgeToEdge` on the mosaic/list pages, `immersiveSticky` while the player is open (through `SystemBarsController`, called from the same lifecycle hook as `ScreenAwakeController` — never from `build`, never `setState`), and `MediaQuery.paddingOf`/`viewPaddingOf` added to the OSD panel, the always-on clock, the guide header and the top of every list page. `targetSdk = flutter.targetSdkVersion`, so Android 15's forced edge-to-edge applies: a layout that ignores `padding` puts the clock under the status bar. `client/android/app/src/main/res/values*/styles.xml` may need `android:windowLayoutInDisplayCutoutMode = shortEdges` for the player to use the notch area for video.
- **Rotation.** `android:configChanges` already contains `orientation|screenSize|smallestScreenSize|density|screenLayout` → no Activity recreation, video survives, only layout boxes change. Keep it. No orientation lock (§1).
- **Back gesture vs the app's Back handling.** Keep `android:enableOnBackInvokedCallback` **unset/false** so that the predictive-back gesture still arrives as `KEYCODE_BACK` → `LogicalKeyboardKey.goBack` → `InputAction.back` → handled by the page or swallowed by `BackKeyGuard`. That keeps the phone's behaviour identical to the TV remote's and keeps the "exit only through the dialog" guarantee. Verify on the lab with a log line (the edge swipe must produce the same handler as the remote's Back); the cost is no predictive-back preview animation, which is acceptable. If it is ever flipped on, `BackKeyGuard` must be replaced by a `PopScope` and this whole paragraph with it.
- **Backgrounding.** Already correct and must not regress: `hidden`/`paused` tears the channel down through `_switchToChannelIndex(load: false)` and drops the mosaic preview; `inactive` keeps playing; `resumed` reloads at the point the guide has reached. On a phone this happens constantly (every notification shade pull, every app switch) — `player_background_test` and `app_background_test` are the guards, and they stay.
- **Keep-awake.** `wakelock_plus` → `FLAG_KEEP_SCREEN_ON`, held only while playing *and* foregrounded, released on leaving the player and on hide. Correct for a phone; nothing to change.
- **Soft keyboard.** Only Settings → Connection, the pairing pages and the plugin source dialog have text input. `windowSoftInputMode="adjustResize"` plus `Scaffold.resizeToAvoidBottomInset` (default) is enough; the player has no text input, so `immersiveSticky` never fights the keyboard. Check the plugin dialog's fixed `maxHeight` (§3).
- **Audio focus — a real phone-only defect, deliberately out of scope.** Players are created with `mixWithOthers` (needed so the crossfade's two players do not pause each other). On a TV there is nothing else playing; on a phone the channel will play *over* a running music/podcast app instead of pausing it. Fixing it means an app-level audio session that owns one focus request for both crossfade players — a separate slice, and it must not be smuggled into this plan.
- **No authoring on a phone — confirmed.** The phone gets the same device token, and the writable surface stays exactly what `isDeviceOrSessionPath` allows for a device: declared plugin source settings, plugin on/off/remove. Channel/block/schedule authoring, `POST /media-sources`, `POST /plugins/{id}/install` and the MCP surface stay web UI + session only. Nothing in this plan adds a client route or a write.
- **One more phone-only path worth naming:** `ProcessAppExit.terminate` already uses `SystemNavigator.pop()` on Android, so "Exit" at the mosaic finishes the Activity the way the platform expects. Nothing to change.

---

## 9. Staged implementation, riskiest assumption first

Each slice is independently shippable and leaves the TV profile green. "TV guard" names the suite that must pass unchanged on the TV profile after the slice.

| # | Slice | Riskiest assumption it settles | Files | Proof |
|---|---|---|---|---|
| **1** | **Form-factor seam, no visual change anywhere** | *That a per-process form factor plus defaults equal to today's constants can be introduced without moving a single TV pixel.* If this fails, the whole "one layout" premise is wrong and the phone needs a forked player page. | `lib/ui/form_factor.dart` (new), `lib/ui/ui_scale.dart`, `lib/main.dart` (`PlayheadSpikeApp.builder`, `_InitialRouteResolverState._resolve`), `test/form_factor_test.dart`, `test/ui_scale_test.dart` | unit: form factor matrix incl. Linux at 320×180/640×360/800×450 → tv · golden: **every existing image in `test/ui_scale*_golden_test.dart` unchanged, byte-identical** · TV guard: `osd_test exit_flow_test mosaic_test`. Devices: Android only, resolved in the boot resolver — **no new `await` before `runApp`**, `prefetchFirstChannel` untouched. |
| **2** | **Phone OSD metrics + tap on every OSD control** | *That the OSD can change shape per form factor without touching the video contract.* The volume of `ui(px)` literals is the risk; `OsdMetrics.of(tv)` is the regression pin. | `lib/ui/osd_metrics.dart` (new), `lib/widgets/osd_overlay.dart` (`_button`, `_channelCard`, `_chip`, `_logo`, panel padding, per-control `onTap`), `lib/ui/ui_scale.dart` (`uiTouch`), `test/osd_metrics_test.dart`, `test/phone_layout_golden_test.dart` | unit: metrics equality for tv + fit for phone · golden: tv OSD images unchanged; new phone OSD at 4 sizes · lab: `osd_test osd_elapsed_test osd_filler_test` on **both** profiles, new `phone_layout_test`. |
| **3** | **One interactive layer in the player's `Stack` + gestures** | **The riskiest single assumption in the plan:** that adding a pointer-handling `Positioned.fill` child above the video does not revive the fvp/libmdk compositing bug that `AGENTS.md` documents (the bare-Stack-child black screen). | `lib/widgets/player_gestures.dart` (new), `lib/main.dart` (one `Positioned.fill` child, `enabled` driven off `_epgOpenNotifier` + `_trackPickerOpenNotifier`), `test/player_gestures_test.dart` | unit/widget: gesture disambiguation · lab: `osd_test player_background_test app_background_test` on the TV profile (a real texture, real playback) + new `phone_touch_test`. If the texture regresses, the fallback is a `Listener` on the video's own ancestor, and this slice stops the plan. |
| **4** | **Safe areas, system bars, insets** | *That `immersiveSticky` + `padding` can be applied from a lifecycle hook without rebuilding the page.* | `lib/services/system_bars.dart` (new), `lib/main.dart` (player route open/close), `lib/widgets/osd_overlay.dart` + `clock_overlay.dart` + `epg_overlay.dart` (insets), `lib/pages/*` (`SafeArea` for list pages), `android/app/src/main/res/values*/styles.xml` (cutout mode), `test/system_bars_test.dart` (new) | unit: controller edge-triggering with a fake · golden: cutout-padded cases · lab: `phone_layout_test` on `-p phone`, `fullscreen_test focus_lifecycle_test` on TV. |
| **5** | **Guide metrics + touch (drag, pinch, cell tap)** | *That the guide's 90-min span and 72 px channel column keep `buildGuideCells` semantics and the opening-frame budget.* | `lib/ui/guide_metrics.dart` (new), `lib/widgets/epg_overlay.dart` (`_visibleSpan`/`_channelColumnWidth` from metrics, `onTap` per cell, horizontal drag on `_hOffsetMinutes`, pinch, close button), `test/guide_metrics_test.dart`, `test/phone_layout_golden_test.dart` | unit: cell widths at 360/780 dp · golden: tv EPG images unchanged + new phone guide · lab: `epg_overlay_test epg_live_nav_test epg_filler_test epg_gap_test` on TV, `perf_boot_epg_test` on a phone run (opening frame), `epg_layout_test` explicitly **TV-only**. |
| **6** | **Mosaic list + `mosaicMove` + tile extraction** | *That the mosaic's selection/preview logic survives a second view, and that extracting the tile builder is behavior-preserving.* | `lib/mosaic_tiles.dart` (new), `lib/mosaic_nav.dart` (`mosaicMove`), `lib/widgets/phone_mosaic_view.dart` (new), `lib/pages/mosaic_page.dart` (one seam), `test/mosaic_tiles_test.dart`, `test/mosaic_nav_test.dart` | unit: both modes · golden: ring images unchanged + new list images · lab: `mosaic_test` on both profiles, `mosaic_nav_scale_test` **TV-only**. |
| **7** | **Touch on the remaining overlays + list-page density** (track picker, dimmer dialog, update banner/changelog, plugin settings dialog height, Settings insets) | *That the four dead-end screens from §2 are the complete list.* | `lib/widgets/track_selector_overlay.dart`, `lib/widgets/dimmer_level_dialog.dart`, `lib/widgets/update_overlay.dart`, `lib/widgets/plugin_source_settings_dialog.dart`, `lib/ui/ui_scale.dart` (`UiScaled` phone behaviour), `test/phone_typography_test.dart` | widget: tap each option/± /dismiss; typography floor · golden: tv track-picker/update images unchanged + phone variants · lab: `track_selector_test dimmer_test update_banner_test settings_navigation_pages_visual_test` on phone, same on TV. |
| **8** | **Lab bookkeeping, docs, phone census** | *That "phone works" can be evidenced, not asserted.* | `client/tool/run_android_suites.sh` (`TV_ONLY` set), `client/tool/suite_map.tsv` (new lines), `docs/ROADMAP.md` Phase 6 (Android build item), `AGENTS.md` ("Client app structure": the form-factor seam, the metrics objects, the "form factor is per-process, metrics are per-size" rule, and the touch-never-replaces-keys rule), `docs/ARCHITECTURE.md` (a "Client form factors" section beside "Mosaic geometry, navigation and UI scale") | `tool/run_android_suites.sh -p phone` full census → `results.json` for every suite; `tool/run_android_suites.sh` (TV) census unchanged. A claim about the phone needs the `results.json`, per the repo's verdict contract. |

**Reusable as-is, no change at all:** the whole video path (`lib/video/*`, `ExoPairedPlayback`, `PairedVideos.kt`, `StreamCache`, prefetch, preload, gap slate, stall watcher, `playhead_clock`), the offline cache and syncer, `epg_guide.dart` and `osd_subject.dart`, `watermark_overlay.dart`, `dimmer.dart` + `dimmer_overlay.dart`, `clock.dart` + `clock_overlay.dart`, `screen_awake.dart`, `app_exit.dart`, `back_key_guard.dart`, `input/input_action.dart` (bindings and aliases), `SettingsService`, `pairing_*`, `update/*`, `art_image.dart` / `channel_logo.dart` / `ArtCache`, and every `pages/*` page's structure.

---

## What I could not verify by reading the code

- **Real dp sizes on a real phone.** Every number above is derived from the constants in the source (`_visibleSpan`, `_channelColumnWidth`, `_rowHeight`, the OSD's paddings and row heights, `mosaicFrameUnit*`, `epgThumbnail*`, `uiScaleMin`). The OSD panel height in particular is a sum of paddings and row heights I computed, not a measurement — the first golden in slice 2 is what turns it into a fact. `flutter test --update-goldens` was not run.
- **Whether `UiScale` at 0.6 actually looks as bad on a device as the arithmetic says.** Ahem-font goldens show layout, not legibility, and I have no screenshot from a phone.
- **Whether the phone profile's `wm size` on the lab is landscape or portrait by default.** `android_lab.sh` says "phone, the AVD's own geometry"; the actual AVD config is not in this repository. Which orientation the phone census runs in, and whether the suite has to force one, is unresolved.
- **Whether Android delivers the back *gesture* as `KEYCODE_BACK`/`goBack` on the Flutter version pinned here**, with `enableOnBackInvokedCallback` unset. I reasoned from the manifest (the attribute is absent) and from the existing `goBack` handling, but I did not run a device.
- **Whether `SystemUiMode.immersiveSticky` fights `FlutterActivity`'s own insets handling on this Flutter version**, and whether the Activity's `configChanges` list is complete for a phone-foldable (`screenLayout` is there; `smallestScreenSize` is there).
- **The real cost of the guide's opening frame at the phone metrics.** Fewer visible cells (90 min instead of 180) should make it cheaper, but the "titles fill in over frames" tuning (`_titleRowsPerFrame = 2`) was measured on a TV box and I have no phone measurement.
- **Audio focus on a phone.** The `mixWithOthers` consequence (channel plays over a music app) is inference from `ARCHITECTURE.md` "ExoPlayer engine"; I did not test it, and it affects only the ExoPlayer path — the `fvp` path's audio behaviour on Android is likewise unverified here.
- **Whether `fvp` on a phone's GPU has the same black-picture class of problem as the TV box** that motivated the opt-in ExoPlayer engine (`AGENTS.md` gotcha). The phone may need ExoPlayer as a *default*, not an option — that is a decision for a real device run, and it would change the "Settings → Video player (Android only)" row's default per form factor, which this plan does not propose.
- **The `404`-shaped question of `docs/ARCHITECTURE.md` "Client offline cache" wording** ("the same engine on Linux/Windows now and Android/Android TV later") suggests the Android cache path is newer than the doc; I did not confirm the SQLite file location behaviour on a phone's scoped storage beyond `path_provider`.

## Status

All eight slices are implemented and committed (2026-11-10):

1. form factor seam — `lib/ui/form_factor.dart`, phone/tv resolved once at boot;
2. OSD metrics and touch on its controls — `lib/ui/osd_metrics.dart`;
3. the gesture layer over the picture — `lib/widgets/player_gestures.dart`;
4. safe area and system bars — `lib/services/system_bars.dart`, `viewPadding`;
5. guide metrics and its two touch paths — `lib/ui/guide_metrics.dart`;
6. the mosaic as a column — `lib/mosaic_tiles.dart`, `widgets/phone_mosaic_view.dart`;
7. the overlays a finger can open and leave — track picker, dim level dialog,
   update banner/changelog, plugin settings box;
8. the phone suites and the lab bookkeeping — `phone_touch_test`,
   `phone_layout_test`, `TV_ONLY`/`PHONE_ONLY` in `run_android_suites.sh`.

What is **not** done, on purpose, and where it is recorded: pinch to change the
guide's span and a Close button in the guide (`docs/ARCHITECTURE.md`, "Client
form factors"); the thumbnail's default per form factor (still a Settings row);
and the mosaic in portrait with a phone that reports a television flag — the flag
is measured on both lab profiles, and the size guard is only a fallback.
