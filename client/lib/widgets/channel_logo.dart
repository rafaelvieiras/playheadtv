// The logo uploaded for a channel (PUT /channels/{id}/logo), drawn wherever the
// client would otherwise print the channel's name: the guide's channel column
// and the OSD header. The bytes come from the art cache (the syncer prefetches
// one file per channel with the backend's credentials), so a channel watched
// offline still shows its logo.
//
// A channel without a logo — or one whose image is not on disk yet and cannot
// be downloaded — falls back to whatever the caller prints instead (normally
// the channel name), never to an empty space: the logo replaces the name, it
// never hides the channel. It never takes focus or pointer events.
import 'package:flutter/material.dart';

import '../cache/offline_runtime.dart';
import '../playhead_api.dart';
import 'art_image.dart';

/// The URL of a channel's logo, or null when it has none (or when the shared
/// runtime, which knows the backend address, is not available: a widget test
/// then just draws the fallback).
String? channelLogoUrl(PlayheadChannel channel) {
  final imageId = channel.logoImageId;
  if (imageId == null || imageId.isEmpty) return null;
  return OfflineRuntime.current?.api.channelLogoUrl(imageId);
}

class ChannelLogo extends StatelessWidget {
  final String url;

  /// Height the logo is scaled to (the width follows its aspect ratio, capped
  /// to the space the caller gives it). Already UiScale-adjusted.
  final double height;

  /// Key given to the inner [Image] (tests find it by key).
  final Key? imageKey;

  /// What the caller shows instead when the image cannot be loaded: normally
  /// the channel name it replaces.
  final WidgetBuilder fallback;

  const ChannelLogo(this.url, {
    super.key,
    required this.height,
    this.imageKey,
    required this.fallback,
  });

  @override
  Widget build(BuildContext context) {
    return ArtImage(
      url,
      imageKey: imageKey,
      fit: BoxFit.contain,
      headers: OfflineRuntime.current?.api.authHeaders ?? const {},
      fallback: fallback,
    );
  }
}

/// Size box that keeps a logo inside [maxHeight] without stretching it: a
/// Container with a fixed height and a bounded width, so the image scales by
/// its own aspect ratio inside it.
class LogoBox extends StatelessWidget {
  final double maxHeight;
  final double? maxWidth;
  final Widget child;

  const LogoBox({super.key, required this.maxHeight, this.maxWidth, required this.child});

  @override
  Widget build(BuildContext context) {
    return ConstrainedBox(
      constraints: BoxConstraints(maxHeight: maxHeight, maxWidth: maxWidth ?? double.infinity),
      child: child,
    );
  }
}
