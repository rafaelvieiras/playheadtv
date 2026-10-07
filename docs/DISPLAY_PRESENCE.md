# Display presence signals (spike for issue #59)

Goal: find out which signals can tell that the display showing the client
stopped showing it (input switched away, standby, output blanked) while the
machine stays on, so the client can pause and later resume at the live
position. This document is the spike: what each signal is, how to read it
without root, what is known, and what is still unverified.

Honesty note: most entries below are **researched only**. Only the
"Measured" section was observed on hardware, and the decisive experiment (a
physical input switch or standby on the TV) has not been run yet. Treat every
"researched" latency and false-positive statement as a hypothesis until the
logger (`client/tool/display_signal_logger.sh`) has been run through the
procedure at the end.

## Summary

- No signal is universal. A TV that switches input or enters standby may
  drop the HDMI hot-plug-detect (HPD) line (the kernel then reports the
  connector as `disconnected`), or may keep HPD high and only stop showing
  the picture. Which one happens is a property of each TV, not of the OS.
- The most portable, no-root candidate on Linux is the DRM connector state in
  sysfs, because it lives in the kernel and is therefore the same under X11
  and Wayland. It only works for TVs that drop HPD.
- HDMI-CEC is the only signal that can tell "the TV is on but another input
  is active" for a TV that keeps HPD high, but it needs hardware that
  exposes a CEC adapter, which is rare on PCs.
- Android and Android TV report the relevant states through the activity
  lifecycle and the display state, so the mobile path is mostly free.

## Signals

"Root" means needed at run time by the client process. "Latency" is the
expected time from the physical event to the signal changing.

### Linux, kernel level (X11 and Wayland alike)

| Signal | How to read | Root? | Known | Unverified | Latency | False-positive risk |
|---|---|---|---|---|---|---|
| DRM connector `status` | Read `/sys/class/drm/card*-<connector>/status` (`connected`/`disconnected`/`unknown`); also `enabled`, `dpms`, and the size of `edid` | No (world-readable) | Files exist and are readable by any user (measured). The kernel documents `status` as the connector's detected state; `echo detect > status` forces a re-probe (that write needs root, so the client must not rely on it). Some drivers and cables are known to leave `connected` after an unplug (see the i915/amd bug reports in Sources) | Whether a given TV drops HPD on input switch and on standby. Whether the kernel updates `status` without a poll when HPD toggles on the test hardware | Researched: HPD interrupt, so well under a second on drivers that handle it; seconds on drivers that poll (connector poll runs about every 10 s) | Medium: an HDMI renegotiation or a mode switch toggles it briefly (needs the grace period); a TV that keeps HPD high never triggers it; an internal panel stays `connected` with the lid closed (measured), so the connector to watch must be the one the window is on, not "any connector" |
| DRM uevents | Netlink uevent (`SUBSYSTEM=drm`, `HOTPLUG=1`, `CONNECTOR=<id>`) via `udevadm monitor -s drm` or a raw `NETLINK_KOBJECT_UEVENT` socket | No for the kernel uevents (any user may bind the netlink group); udev-processed events need a running udevd | `udevadm monitor` is available on the test system (measured). The event carries no new state, the reader must re-read `status` | Event delivery on the test hardware; behaviour on systems without udev (a plain netlink socket still receives kernel uevents) | Same as `status` plus negligible | Same as `status`. Useful only to avoid a 1 s poll; a 1 s poll of sysfs is cheap and simpler in Dart |
| DRM debugfs (`/sys/kernel/debug/dri/*`) | `i915_display_info`, per-connector files | Yes (debugfs is root-only) | Not mounted on the test system (measured) | Everything | n/a | Not usable: needs root, and debugfs is often absent |

### Linux, X11

| Signal | How to read | Root? | Known | Unverified | Latency | False-positive risk |
|---|---|---|---|---|---|---|
| RandR output state | `xrandr --current` (the server's cached view), or the RandR extension: `RRSelectInput` with `RRNotify`/`RROutputChangeNotify`, reading `XRRGetOutputInfo().connection` | No | The test X server lists every connector with connected/disconnected state and the mode in use (measured). RandR is documented to send output change events when the server learns of a hotplug | Whether the server updates its cached state on an input switch (it only learns what the kernel reports, so it is bounded by the DRM row); Dart has no binding, so it means `dart:ffi` to `libXrandr`/`libX11` or spawning `xrandr` | Bounded by the DRM row; the server reacts to the kernel hotplug uevent | Same as DRM. Note a compositing or auto-configuring desktop may re-lay-out the screen itself on the event |
| DPMS state | `xset q` ("DPMS is Enabled/Disabled", "Monitor is On/Standby/Suspend/Off"); extension `DPMSInfo` | No | Reported correctly by `xset q` (measured: DPMS was disabled on the test system, which is common on a kiosk). DPMS only reflects the machine's own blanking, which the display wake lock (#49) deliberately prevents | Whether anything blanks the output while the client plays | Immediate | Low, but it answers a different question: it does not change when a TV switches input |
| Window visibility | `AppLifecycleState` (see below) | No | Works for minimize and hide | Whether a window on a switched-away output counts as hidden: it does not, the X server keeps mapping it | n/a | Does not fire for a switched-away TV |

### Linux, Wayland

| Signal | How to read | Root? | Known | Unverified | Latency | False-positive risk |
|---|---|---|---|---|---|---|
| `wl_output` removal | `wl_registry.global_remove` for the output, or `wl_output` events | No | Compositors remove an output when the kernel reports `disconnected`, so this is the DRM row seen through the compositor | Per-compositor behaviour (some keep a "phantom" output, some re-arrange windows on removal); no Dart binding exists, so it needs a native helper | Bounded by the DRM row | Same as DRM |
| `ext-idle-notify-v1` | Compositor idle notification | No | Standard protocol, widely implemented | Not useful here: it reports user inactivity, not display state, and wake locks inhibit it | n/a | High: unrelated to the display |
| `wlr-output-power-management-unstable-v1` / KDE `dpms` | Set (and in KDE query) the output power mode | No for clients the compositor allows | wlroots-based compositors and KDE expose them. The protocol is aimed at shells that turn outputs off, not at observing a TV | Whether a client can *observe* a mode set by someone else; GNOME does not implement either | n/a | Not a presence signal |
| Compositor-specific D-Bus (e.g. `org.gnome.Mutter.DisplayConfig` `MonitorsChanged`) | D-Bus signal | No (session bus) | Fires when the monitor set changes, i.e. the DRM row seen through the desktop; available through the `dbus` package | Per-desktop behaviour | Bounded by the DRM row | Same as DRM |

Because every Wayland path ends at the kernel connector state, reading DRM
`status` directly is the only implementation that does not depend on the
compositor.

### HDMI-CEC

| Signal | How to read | Root? | Known | Unverified | Latency | False-positive risk |
|---|---|---|---|---|---|---|
| Kernel CEC device | `/dev/cec*` (kernel CEC framework since 4.8), `cec-ctl --monitor`, or `libcec`/`cec-client`; messages of interest: `Report Power Status` (TV on/standby), `Active Source`/`Routing Change`/`Set Stream Path` (which input is active), `Inactive Source` | Typically the device node is group-accessible (usually `video`); not world-readable by default | On the test system `cec-ctl` and `cec-client` are installed but no `/dev/cec*` node exists and `/sys/class/cec` is absent (measured): the hardware exposes no CEC adapter. Researched: Intel integrated GPUs generally do not expose CEC on HDMI; CEC works with a Raspberry Pi, with some Intel/AMD DisplayPort-to-HDMI adapters that tunnel CEC, or with a USB CEC adapter | Group ownership and permissions of the node on a real adapter; whether a given TV sends `Active Source` for other devices on input switch; vendor quirks (many TVs ignore or rename CEC features) | Researched: a message within about a second of the remote key press | Low for explicit standby; medium for "active source changed", since some devices re-assert themselves. Needs the client to take part in the CEC bus, which has side effects (it can wake the TV or switch its input) |
| libcec from Dart | `dart:ffi` to `libcec`, or spawn `cec-client -d 8`/`cec-ctl --monitor` and parse stdout | As above | n/a | Packaging: libcec is not part of the AppImage today | n/a | n/a |

### Window and application lifecycle

| Signal | How to read | Root? | Known | Unverified | Latency | False-positive risk |
|---|---|---|---|---|---|---|
| Flutter `AppLifecycleState` on Linux | `WidgetsBindingObserver.didChangeAppLifecycleState` | No | The Linux (GTK) embedder maps window state to `resumed`, `inactive` (no focus) and `hidden` (iconified/not visible); it does not produce `paused` | Behaviour when the window is on another workspace (depends on the window manager) | Immediate | `inactive` fires on every focus loss and must not pause; only `hidden` is a candidate; it does not fire when the TV merely stops showing the output |
| Flutter `AppLifecycleState` on Android / Android TV | Same observer | No | Android reports `paused`/`hidden` for `onPause`/`onStop`, including screen off and the user leaving the app | Android TV home-key and standby edge cases | Immediate | Low |

### Android

| Signal | How to read | Root? | Known | Unverified | Latency | False-positive risk |
|---|---|---|---|---|---|---|
| Activity lifecycle (`onPause`/`onStop`) | Flutter `AppLifecycleState` (above) | No | Standard; screen off and standby stop the activity | n/a | Immediate | Low |
| Display state | `DisplayManager.DisplayListener` (`onDisplayChanged`, `onDisplayRemoved`), `Display.getState()` (`STATE_OFF`, `STATE_DOZE`, ...), `PowerManager.isInteractive` | No | The framework recreates display state on a hotplug and notifies listeners | Whether an Android TV keeps the display `ON` while the TV is on another input | Immediate on the device side | Low for screen off; does not see the TV switching input |
| Android TV HDMI-CEC | `HdmiControlManager` (`HdmiPlaybackClient`, `HdmiTvClient`) | The `HDMI_CEC` permission is `signatureOrSystem`: system or privileged apps only | Not available to a normal app (researched in AOSP docs) | n/a | n/a | Not usable. On Android TV the platform itself puts the device to standby when the TV does, which then arrives as the lifecycle signal |

Android needs a platform channel (or a Flutter plugin) only if the Dart
lifecycle observer proves insufficient; the first iteration can rely on
`AppLifecycleState`.

### Opt-in fallback

"No remote or keyboard input for N hours while playing" ("Still watching?")
works everywhere and needs no signal, at the cost of being slow and
user-visible. It is the last resort for setups where nothing above fires,
and it must be opt-in.

## Packages and access from Dart

| Signal | Reachable from Dart with |
|---|---|
| DRM `status` | Plain `dart:io` file reads and a timer; no dependency |
| DRM uevents | `dart:ffi` to `libc` (`socket(AF_NETLINK, ...)`), or spawn `udevadm monitor` |
| RandR | `dart:ffi` to `libXrandr`; or spawn `xrandr --current` and parse |
| Mutter / KDE signals | The `dbus` package (already a dependency for the screen-saver inhibitor) |
| `wl_output` | `dart:ffi` to `libwayland-client`; not worth it over DRM `status` |
| CEC | `dart:ffi` to `libcec`, or `ioctl` on `/dev/cec*` via `dart:ffi`, or spawn `cec-ctl --monitor` |
| Lifecycle | Flutter's `WidgetsBindingObserver`; no dependency |
| Android display state | Small Kotlin platform channel around `DisplayManager` |

## Recommended implementation order

1. **Lifecycle** (`AppLifecycleState.hidden`/`paused`): free on Android and
   Linux, no root, covers screen off and minimize. Wire it first behind the
   `DisplayPresence` interface.
2. **DRM connector `status` by polling sysfs** (about every 1-2 s, no root,
   same on X11 and Wayland): the first working Linux signal. Map the window
   to its connector (match the output the window is on; on a single-output
   TV setup, ignore internal panels). Treat `disconnected` and a changed
   `enabled` as "gone". Keep the poll interval and the connector choice
   configurable. Prefer polling over uevents: it is dependency free and the
   state is what matters, not the edge.
3. **Opt-in "Still watching?"** fallback, because step 2 is useless for TVs
   that keep HPD high.
4. **CEC** (opt-in, only when a `/dev/cec*` node exists and is accessible):
   the only signal for the keep-HPD-high TVs, but limited to rare hardware.
5. RandR/Wayland/D-Bus signals only if step 2 turns out to miss events that
   they catch (unlikely, they derive from the same kernel state).

## Recommended default grace period

**30 seconds** before pausing, configurable, applied to every "gone" signal.
Rationale (researched, to be confirmed by measurement): HDMI renegotiation,
mode switches and AV receivers re-syncing can toggle HPD for several seconds,
and a TV input switch followed by switching back is usually under 10 s.
Resume should be immediate on "back" (no grace), through the same resync
used after a gap. For CEC `Report Power Status: standby` a shorter grace
(about 5 s) is reasonable, since it is an explicit message rather than an
electrical event; confirm after measuring.

## Measured vs researched only

### Measured (read-only, no physical switch)

On a Linux x86_64 test machine with an Intel integrated GPU (`i915` driver,
6.x kernel), a TV on one HDMI connector, the internal panel closed, X11 with
a window manager, no systemd:

- `/sys/class/drm/*/status`, `enabled`, `dpms` and `edid` are readable
  without any special handling; the TV connector read `connected`, `enabled`,
  `On`, with a 256-byte EDID; unused connectors read `disconnected`,
  `disabled`, `Off`, EDID size 0.
- The internal panel read `connected`/`enabled` with the lid closed: a rule
  of "any connected output" would never see a change from it, and must
  ignore it.
- X11 RandR lists every connector with its state and current mode, and
  places the closed panel inside the same desktop; `xrandr --current` and
  `xset q` work without a special authority file there.
- DPMS was disabled on that X server (so DPMS cannot be a presence signal
  there).
- `udevadm monitor` exists and starts.
- `cec-ctl` and `cec-client` binaries exist, but there is no `/dev/cec*` and
  no `/sys/class/cec`: no CEC adapter is exposed by that hardware, so CEC is
  not an option on it.
- `/sys/kernel/debug/dri` is not available.
- The logger ran for a few seconds and produced one timestamped line per
  second with all the above, and cleaned up its helper process.

### Researched only (not verified on hardware)

- Whether any given TV drops HPD on input switch or standby, and how fast
  the kernel then flips `status`. This is the decisive unknown and needs the
  procedure below.
- Whether the X server and compositors propagate the change.
- All latency figures, the false-positive notes on HDMI renegotiation, and
  the default grace period.
- Everything about CEC message behaviour, `/dev/cec*` permissions, and which
  adapters expose CEC.
- Everything about Wayland compositors and Android (no Wayland session and no
  Android device were available to the spike).
- `AppLifecycleState.hidden` behaviour per window manager.

## Procedure to finish the measurement (needs a human at the TV)

1. Copy `client/tool/display_signal_logger.sh` to the test machine (a
   temporary directory is enough; it writes only to its output directory).
2. Run it for about 10 minutes, with the client playing or not, from a shell
   on the machine (over SSH is fine):

   `DISPLAY=:0 sh display_signal_logger.sh 600 /tmp/display_signal_log`

   Set `XAUTHORITY` too if the X server requires it. `INTERVAL=0.5` gives a
   finer poll where `sleep` supports fractions.
3. During the window, with a stopwatch or by noting the clock time, do each
   of these in turn, waiting about 30 s between actions, and write down the
   time of each action:
   - switch the TV to another HDMI input, wait, switch back;
   - put the TV in standby with its remote, wait, wake it;
   - power the TV off with its button (not the remote) if it differs;
   - if available, power an AV receiver in the chain off and on.
4. Read `changes.log` (only the transitions), `signals.log` (every second)
   and `udev.log` (raw DRM uevents). For each action record which of these
   changed and how many seconds after the action: connector `status`,
   `enabled`, EDID size, X11 `connected` state, DPMS, and any uevent. Note
   whether `status` flapped and for how long (this sets the grace period).
5. Remove the output directory afterwards and record the TV's behaviour here
   as conditions ("a TV that keeps HPD high on input switch"), without
   brand or model.

## Sources

- DRM connector status and hotplug behaviour: kernel bug reports
  [i915 HDMI status stays connected after disconnection](https://lkml.iu.edu/hypermail/linux/kernel/1807.0/04580.html),
  [hdmi unplug not changing connector status](https://gitlab.freedesktop.org/drm/amd/issues/808),
  [vc4 hotplug connector status](https://lkml.iu.edu/hypermail/linux/kernel/2109.1/08705.html)
- Kernel CEC framework: [CEC Kernel Support](https://www.kernel.org/doc/html/v5.4/media/kapi/cec-core.html)
- CEC opcodes: [Understanding CEC Opcodes](https://support.pulse-eight.com/support/solutions/articles/30000053359-understanding-cec-opcodes)
- Android HDMI-CEC service and permission level: [HDMI-CEC control service](https://source.android.com/docs/devices/tv/hdmi-cec)
- Android display listener: [DisplayManager.DisplayListener](https://developer.android.com/reference/android/hardware/display/DisplayManager.DisplayListener),
  [Hotplug handling](https://source.android.com/devices/graphics/hotplug)
- Wayland power and idle protocols: [wlr-output-power-management](https://wayland.app/protocols/wlr-output-power-management-unstable-v1),
  [KDE dpms protocol](https://lxr.kde.org/source/libraries/plasma-wayland-protocols/src/protocols/dpms.xml)
- Flutter lifecycle: [AppLifecycleState](https://api.flutter.dev/flutter/dart-ui/AppLifecycleState.html),
  [Linux embedder window state monitor](https://api.flutter.dev/linux-embedder/fl__window__state__monitor_8cc.html)
