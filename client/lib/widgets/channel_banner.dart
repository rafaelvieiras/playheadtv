// The channel banner: the compact card a channel change puts in a corner of the
// screen — channel logo and number, the program on air, its description and its
// time span. It replaces the full-width OSD as the *automatic* announcement of a
// channel change (the OSD stays what Select opens, on the control it always
// had); see docs/ARCHITECTURE.md "Channel change".
//
// It is shown while the channel is switching, and for a configurable while
// after the picture comes up, so the programme is announced without covering the
// bottom of the frame. It never takes focus and never takes pointer events.
//
// Same non-setState convention as the OSD and the guide: the parent owns a
// ValueNotifier and this widget is built once per state change, never rebuilt by
// a page-level setState (AGENTS.md, the GL-texture gotcha).
import 'package:flutter/material.dart';

import 'app_icons.dart';
import 'channel_logo.dart';
import '../ui/ui_scale.dart';

/// Which corner the banner sits in (Settings -> Channel banner corner).
enum BannerCorner {
  topLeft,
  topRight,
  bottomLeft,
  bottomRight;

  Alignment get alignment => switch (this) {
        BannerCorner.topLeft => Alignment.topLeft,
        BannerCorner.topRight => Alignment.topRight,
        BannerCorner.bottomLeft => Alignment.bottomLeft,
        BannerCorner.bottomRight => Alignment.bottomRight,
      };

  String get label => switch (this) {
        BannerCorner.topLeft => 'Top left',
        BannerCorner.topRight => 'Top right',
        BannerCorner.bottomLeft => 'Bottom left',
        BannerCorner.bottomRight => 'Bottom right',
      };

  static BannerCorner fromName(String? name) =>
      BannerCorner.values.firstWhere((c) => c.name == name, orElse: () => BannerCorner.topLeft);
}

/// What the banner says about one channel change. Every program field is
/// optional: a channel whose guide is not cached yet still gets its logo, number
/// and name, which is more than the plain "number · name" line it used to get.
class ChannelBannerData {
  final int number;
  final String name;

  /// The uploaded channel logo, or the channel's own guide art, or null (the
  /// banner then draws the number only).
  final String? logoUrl;

  /// "Series · S1E3 · Title" for a program, the channel's name when nothing is
  /// scheduled (a gap), null while the guide is not known yet.
  final String? programTitle;
  final String? description;
  final DateTime? startAt;
  final DateTime? endAt;

  const ChannelBannerData({
    required this.number,
    required this.name,
    this.logoUrl,
    this.programTitle,
    this.description,
    this.startAt,
    this.endAt,
  });
}

class ChannelBanner extends StatelessWidget {
  final ChannelBannerData data;
  final BannerCorner corner;

  /// The wall clock; tests pass a fixed one.
  final DateTime Function() now;

  /// Keys the widget for tests (`channel-banner`), and keeps the loading
  /// contract (`loading-osd`) while the channel has no picture yet: a suite that
  /// asserted "the channel is announced while it loads" keeps finding it.
  const ChannelBanner({
    super.key,
    required this.data,
    required this.corner,
    this.now = DateTime.now,
  });

  static String _hhmm(DateTime dt) {
    final l = dt.toLocal();
    return '${l.hour.toString().padLeft(2, '0')}:${l.minute.toString().padLeft(2, '0')}';
  }

  static String _left(DateTime end, DateTime now) {
    final left = end.difference(now);
    if (left.isNegative) return 'ended';
    final minutes = left.inMinutes;
    if (minutes < 1) return 'ends now';
    if (minutes < 60) return '$minutes min left';
    final h = minutes ~/ 60;
    final m = minutes % 60;
    return m == 0 ? '${h}h left' : '${h}h ${m}min left';
  }

  @override
  Widget build(BuildContext context) {
    // The app-wide interface scale, the same one the OSD and the guide use: a
    // viewer who raised it raised it for the banner too.
    double u(double px) => context.ui(px);
    final padding = MediaQuery.viewPaddingOf(context);
    final margin = u(48);

    final program = data.programTitle;
    final startAt = data.startAt;
    final endAt = data.endAt;
    final span = (startAt != null && endAt != null)
        ? '${_hhmm(startAt)}\u2013${_hhmm(endAt)}  \u00b7  ${_left(endAt, now())}'
        : null;

    final logoUrl = data.logoUrl;
    final logo = SizedBox(
      width: u(72),
      height: u(72),
      child: logoUrl == null
          ? Icon(AppIcons.channels, color: Colors.white70, size: u(40))
          : ChannelLogo(logoUrl, height: u(64), imageKey: const Key('channel-banner-logo'), fallback: (_) => const SizedBox.shrink()),
    );

    final text = Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [
        Row(
          children: [
            Text(
              '${data.number}',
              key: const Key('channel-banner-number'),
              style: TextStyle(color: Colors.white, fontSize: u(24), fontWeight: FontWeight.bold),
            ),
            SizedBox(width: u(10)),
            Expanded(
              child: Text(
                data.name.toUpperCase(),
                key: const Key('channel-banner-name'),
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(color: Colors.white70, fontSize: u(18), letterSpacing: 1.2),
              ),
            ),
          ],
        ),
        if (program != null) ...[
          SizedBox(height: u(6)),
          Text(
            program,
            key: const Key('channel-banner-program'),
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
            style: TextStyle(color: Colors.white, fontSize: u(22), fontWeight: FontWeight.bold, height: 1.2),
          ),
        ],
        if (span != null) ...[
          SizedBox(height: u(4)),
          Text(
            span,
            key: const Key('channel-banner-times'),
            style: TextStyle(color: Colors.white60, fontSize: u(15)),
          ),
        ],
        if (data.description != null && data.description!.isNotEmpty) ...[
          SizedBox(height: u(8)),
          Text(
            data.description!,
            key: const Key('channel-banner-description'),
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
            style: TextStyle(color: Colors.white70, fontSize: u(15), height: 1.3),
          ),
        ],
      ],
    );

    return IgnorePointer(
      child: Align(
        alignment: corner.alignment,
        child: Padding(
          padding: EdgeInsets.only(
            left: margin + padding.left,
            right: margin + padding.right,
            top: margin + padding.top,
            bottom: margin + padding.bottom,
          ),
          child: ConstrainedBox(
            constraints: BoxConstraints(maxWidth: u(640)),
            child: Container(
              key: const Key('channel-banner'),
              padding: EdgeInsets.all(u(18)),
              decoration: BoxDecoration(
                color: Colors.black.withValues(alpha: 0.82),
                borderRadius: BorderRadius.circular(u(10)),
                border: Border.all(color: Colors.white12),
              ),
              child: Row(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  logo,
                  SizedBox(width: u(16)),
                  Flexible(child: text),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}
