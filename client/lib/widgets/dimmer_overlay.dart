// The dimmer layer: a black ColoredBox of the dimmer's opacity above
// everything else on the player page (video, OSD, EPG, pickers). Driven by
// ValueNotifiers through a ListenableBuilder, never setState on the page
// (AGENTS.md GL-texture notes). It ignores pointers and never takes focus.
import 'package:flutter/material.dart';

import '../dimmer.dart';

class DimmerOverlay extends StatelessWidget {
  const DimmerOverlay({super.key});

  @override
  Widget build(BuildContext context) {
    // Positioned.fill in every branch: a bare Stack child breaks the video
    // texture composition (see the EPG overlay's comment in main.dart).
    return Positioned.fill(
      child: IgnorePointer(
        child: ListenableBuilder(
          listenable: Listenable.merge([Dimmer.active, Dimmer.level]),
          builder: (context, _) {
            final opacity = dimOpacity(active: Dimmer.active.value, levelPercent: Dimmer.level.value);
            if (opacity <= 0) return const SizedBox.shrink();
            return ColoredBox(
              key: const Key('dimmer-overlay'),
              color: Color.fromRGBO(0, 0, 0, opacity),
              child: const SizedBox.expand(),
            );
          },
        ),
      ),
    );
  }
}
