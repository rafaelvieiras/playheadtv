// The always-on clock: local time in the player's top-right corner, drawn
// whether the OSD is open or not (see clock.dart for the policy and the
// Settings rows that drive it).
//
// Hidden while the OSD shows: the OSD draws its own clock in the very same
// corner, and two clocks there would both clash and disagree on styling. Also
// hidden while the full-screen guide is open, which has its own time header and
// reads a clock in the corner as a leftover.
//
// Same conventions as the watermark and the dimmer: a Positioned child of the
// player's Stack driven by ValueListenables, never setState on the player page
// (AGENTS.md, the GL-texture gotcha). It takes no pointer events, never takes
// focus, and degrades to nothing: the default is off.
import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import '../clock.dart';
import '../ui/ui_scale.dart';

/// How often the clock recomputes the time. It only shows hours and minutes, so
/// a tick one second apart leaves at most a second of lag on the minute flip
/// while a rebuild stays cheap; the widget skips the rebuild when the minute
/// has not changed anyway.
const clockTick = Duration(seconds: 1);

class ClockOverlay extends StatefulWidget {
  /// The time to show; tests pass a fixed clock.
  final DateTime Function() now;

  /// The OSD's visibility: the clock steps aside while it is on screen.
  final ValueListenable<bool> osdVisible;

  /// Whether the full-screen guide is open (it has its own clock).
  final ValueListenable<bool> guideOpen;

  const ClockOverlay({
    super.key,
    required this.osdVisible,
    required this.guideOpen,
    this.now = DateTime.now,
  });

  @override
  State<ClockOverlay> createState() => _ClockOverlayState();
}

class _ClockOverlayState extends State<ClockOverlay> {
  Timer? _ticker;

  /// The minute last built ("HH:mm" local), so a tick that changes nothing on
  /// screen does not rebuild.
  String _shown = '';

  static String _hhmm(DateTime dt) {
    final local = dt.toLocal();
    return '${local.hour.toString().padLeft(2, '0')}:${local.minute.toString().padLeft(2, '0')}';
  }

  @override
  void initState() {
    super.initState();
    _shown = _hhmm(widget.now());
    _ticker = Timer.periodic(clockTick, (_) {
      if (!mounted) return;
      final next = _hhmm(widget.now());
      if (next == _shown) return;
      setState(() => _shown = next);
    });
  }

  @override
  void dispose() {
    _ticker?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    // Always a Positioned child of the player's Stack, in both branches: a bare
    // zero-sized child breaks the video texture's composition (see the EPG's
    // ValueListenableBuilder in main.dart).
    return Positioned(
      top: context.ui(20),
      right: context.ui(32),
      child: IgnorePointer(
        child: ValueListenableBuilder<bool>(
          valueListenable: ClockOverlaySettings.alwaysVisible,
          builder: (context, always, _) {
            if (!always) return const SizedBox.shrink();
            return ValueListenableBuilder<bool>(
              valueListenable: widget.osdVisible,
              builder: (context, osdVisible, _) {
                if (osdVisible) return const SizedBox.shrink();
                return ValueListenableBuilder<bool>(
                  valueListenable: widget.guideOpen,
                  builder: (context, guideOpen, _) {
                    if (guideOpen) return const SizedBox.shrink();
                    return ValueListenableBuilder<double>(
                      valueListenable: ClockOverlaySettings.scale,
                      builder: (context, scale, _) => ValueListenableBuilder<int>(
                        valueListenable: ClockOverlaySettings.opacityPct,
                        builder: (context, opacityPct, _) => Opacity(
                          opacity: opacityPct / 100,
                          child: Text(
                            _shown,
                            key: const Key('always-on-clock'),
                            style: TextStyle(
                              color: Colors.white,
                              // The OSD's own size, scaled by the user's choice.
                              fontSize: context.ui(28) * scale,
                              fontWeight: FontWeight.w500,
                              // A dark shadow keeps the time readable over a
                              // bright picture at low opacity, without a box.
                              shadows: const [Shadow(color: Colors.black54, blurRadius: 6)],
                            ),
                          ),
                        ),
                      ),
                    );
                  },
                );
              },
            );
          },
        ),
      ),
    );
  }
}
