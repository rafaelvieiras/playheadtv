// The card a channel shows instead of a picture when the program on air cannot
// be decoded on this device.
//
// Not an error screen and not a spinner: nothing is wrong with the channel or
// with the backend, and the channel is not stuck - it is waiting out one program
// it cannot play, and it returns by itself when the next one starts. So the card
// says three things: what cannot play, why, and when the channel is back.
//
// Driven by a ValueListenable (the player's `_unplayableNotice`), never by
// setState on the player page, for the same reason as every other overlay there
// (AGENTS.md, the GL-texture gotcha). It never takes focus and never takes a
// pointer: the remote, the guide and the channel banner keep working, and a
// viewer can simply change channel.
import 'dart:math' as math;

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import '../ui/ui_scale.dart';

/// A program this device cannot play, on screen while the channel waits for the
/// next one.
///
/// The rule it carries: a program the device cannot decode does not play at all.
/// It is not shown black with a frozen frame, and it is not retried until it
/// works - the channel says what happened and comes back on its own when the
/// next program starts (see `_enterUnplayableWait` in main.dart).
@immutable
class UnplayableNotice {
  const UnplayableNotice({
    required this.channelNumber,
    required this.channelName,
    required this.programLabel,
    required this.reason,
    required this.resumeAt,
  });

  final int channelNumber;
  final String channelName;

  /// What the guide calls the program that cannot play.
  final String programLabel;

  /// Why it cannot play, in plain words (`no decoder for mpeg4 at 320x240`).
  final String reason;

  /// When the channel resumes: the end of the program it is waiting out.
  final DateTime resumeAt;

  @override
  bool operator ==(Object other) =>
      other is UnplayableNotice &&
      other.channelNumber == channelNumber &&
      other.channelName == channelName &&
      other.programLabel == programLabel &&
      other.reason == reason &&
      other.resumeAt == resumeAt;

  @override
  int get hashCode =>
      Object.hash(channelNumber, channelName, programLabel, reason, resumeAt);

  @override
  String toString() =>
      'UnplayableNotice($programLabel: $reason, until $resumeAt)';
}

/// Reads a reason as a sentence: it arrives from the decoder check as
/// `no decoder for mpeg4 at 320x240`.
String unplayableReasonText(String reason) {
  final text = reason.trim();
  if (text.isEmpty) return 'This device cannot play the video of this program.';
  return '${text[0].toUpperCase()}${text.substring(1)}.';
}

/// `14:35`, in the device's own local time.
String unplayableClock(DateTime utc) {
  final local = utc.toLocal();
  return '${local.hour.toString().padLeft(2, '0')}:${local.minute.toString().padLeft(2, '0')}';
}

class PlayerUnplayableNotice extends StatelessWidget {
  const PlayerUnplayableNotice({super.key, required this.source});

  final ValueListenable<UnplayableNotice?> source;

  @override
  Widget build(BuildContext context) {
    // Always a Positioned.fill child of the player's Stack: a loose child in a
    // Stack of positioned children would shrink it.
    return Positioned.fill(
      child: ValueListenableBuilder<UnplayableNotice?>(
        valueListenable: source,
        builder: (context, notice, _) {
          if (notice == null) return const SizedBox.shrink();
          final margin = context.ui(48);
          return IgnorePointer(
            // Scrollable, not merely centered: the card is text at television
            // sizes, and a short window (a phone in landscape, a desktop window
            // resized) must show all of it instead of a striped overflow.
            child: LayoutBuilder(
              builder: (context, constraints) => SingleChildScrollView(
                padding: EdgeInsets.all(margin),
                child: ConstrainedBox(
                  constraints: BoxConstraints(
                    minHeight: math.max(0, constraints.maxHeight - margin * 2),
                  ),
                  child: Center(
                    child: Container(
                      key: const Key('unplayable-notice'),
                      constraints: BoxConstraints(maxWidth: context.ui(900)),
                      padding: EdgeInsets.symmetric(
                        horizontal: context.ui(40),
                        vertical: context.ui(32),
                      ),
                      decoration: BoxDecoration(
                        color: Colors.black.withValues(alpha: 0.82),
                        borderRadius: BorderRadius.circular(context.ui(12)),
                      ),
                      child: Column(
                        mainAxisSize: MainAxisSize.min,
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Row(
                            children: [
                              Icon(
                                Icons.info_outline,
                                color: Colors.white70,
                                size: context.ui(34),
                              ),
                              SizedBox(width: context.ui(16)),
                              Expanded(
                                child: Text(
                                  'This program cannot be played on this device',
                                  key: const Key('unplayable-notice-title'),
                                  style: TextStyle(
                                    color: Colors.white,
                                    fontSize: context.ui(30),
                                    fontWeight: FontWeight.bold,
                                  ),
                                ),
                              ),
                            ],
                          ),
                          SizedBox(height: context.ui(20)),
                          Text(
                            notice.programLabel,
                            key: const Key('unplayable-notice-program'),
                            style: TextStyle(
                              color: Colors.white,
                              fontSize: context.ui(26),
                            ),
                          ),
                          SizedBox(height: context.ui(10)),
                          Text(
                            unplayableReasonText(notice.reason),
                            key: const Key('unplayable-notice-reason'),
                            style: TextStyle(
                              color: Colors.white70,
                              fontSize: context.ui(22),
                            ),
                          ),
                          SizedBox(height: context.ui(22)),
                          Text(
                            'Channel ${notice.channelNumber}'
                            '${notice.channelName.isEmpty ? '' : ' · ${notice.channelName}'}'
                            ' is waiting for the next program, back at ${unplayableClock(notice.resumeAt)}.',
                            key: const Key('unplayable-notice-waiting'),
                            style: TextStyle(
                              color: Colors.white70,
                              fontSize: context.ui(22),
                            ),
                          ),
                        ],
                      ),
                    ),
                  ),
                ),
              ),
            ),
          );
        },
      ),
    );
  }
}
