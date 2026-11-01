// An image shown from the on-disk art cache when it is there, from the network
// otherwise (and then cached for next time), with a placeholder when neither
// works. The syncer prefetches the posters of the whole cached window, so with
// the backend (or the media server) out of reach the art is already on disk.
import 'dart:async';

import 'package:flutter/material.dart';

import '../cache/offline_runtime.dart';

class ArtImage extends StatefulWidget {
  final String url;
  final BoxFit fit;

  /// Key given to the inner [Image] (tests find it by key).
  final Key? imageKey;

  /// Headers for the network path: an image served by the Playhead TV backend
  /// (a channel logo) needs the device token; art on the media server does
  /// not. They are not part of the cache key.
  final Map<String, String> headers;

  /// Shown when the image cannot be loaded.
  final WidgetBuilder fallback;

  const ArtImage(
    this.url, {
    super.key,
    this.imageKey,
    this.fit = BoxFit.cover,
    this.headers = const {},
    required this.fallback,
  });

  @override
  State<ArtImage> createState() => _ArtImageState();
}

class _ArtImageState extends State<ArtImage> {
  @override
  Widget build(BuildContext context) {
    final art = OfflineRuntime.current?.art;
    final cached = art?.cached(widget.url);
    if (cached != null) {
      return Image.file(
        cached,
        key: widget.imageKey,
        fit: widget.fit,
        errorBuilder: (context, error, stack) => widget.fallback(context),
      );
    }
    // Not cached yet: show it from the network and keep a copy.
    if (art != null) unawaited(art.fetch(widget.url, headers: widget.headers));
    return Image.network(
      widget.url,
      key: widget.imageKey,
      fit: widget.fit,
      headers: widget.headers.isEmpty ? null : widget.headers,
      errorBuilder: (context, error, stack) => widget.fallback(context),
    );
  }
}
