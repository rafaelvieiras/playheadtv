// The channel watermark: a small image in a corner of the player, above the
// video and below the OSD and the EPG. It is driven by a ValueListenable (the
// player's `_watermarkNotifier`), never by setState on the player page, so a
// change of program never rebuilds the video surfaces (AGENTS.md, the
// GL-texture gotcha). It never takes focus or pointer events, and it degrades
// to nothing, never to an error: an image that is not (yet) on disk or that
// cannot be decoded just means no watermark.
//
// Not drawn on the gap slate, on the loading spinner or on the mosaic previews:
// the player sets the source to null there and the mosaic does not use this
// widget at all.
import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import '../cache/offline_runtime.dart';
import '../ui/ui_scale.dart';
import '../watermark.dart';

/// Finds the image file of a watermark: the art cache first (the syncer
/// prefetched it), a download otherwise. Null when it is not available.
typedef WatermarkImageResolver = FutureOr<File?> Function(
  EpgWatermark watermark,
);

/// The resolver the app uses: the shared offline runtime's art cache.
FutureOr<File?> resolveWatermarkFromRuntime(EpgWatermark watermark) {
  final runtime = OfflineRuntime.current;
  if (runtime == null) return null;
  final url = runtime.api.watermarkImageUrl(watermark.imageId);
  final hit = runtime.art.cached(url);
  if (hit != null) return hit;
  return runtime.art.fetch(url, headers: runtime.api.authHeaders);
}

class WatermarkOverlay extends StatelessWidget {
  final ValueListenable<EpgWatermark?> source;
  final WatermarkImageResolver resolve;

  const WatermarkOverlay({
    super.key,
    required this.source,
    this.resolve = resolveWatermarkFromRuntime,
  });

  @override
  Widget build(BuildContext context) {
    // Always a Positioned.fill child of the player's Stack (never a loose,
    // zero-sized child, which would shrink a Stack that only has positioned
    // children).
    return Positioned.fill(
      child: ValueListenableBuilder<EpgWatermark?>(
        valueListenable: source,
        builder: (context, watermark, _) {
          if (watermark == null) return const SizedBox.shrink();
          // Keyed by the image: another image starts a new lookup, a change of
          // options only re-lays out.
          return _WatermarkImage(
            key: ValueKey(watermark.imageId),
            watermark: watermark,
            resolve: resolve,
          );
        },
      ),
    );
  }
}

class _WatermarkImage extends StatefulWidget {
  final EpgWatermark watermark;
  final WatermarkImageResolver resolve;

  const _WatermarkImage({
    super.key,
    required this.watermark,
    required this.resolve,
  });

  @override
  State<_WatermarkImage> createState() => _WatermarkImageState();
}

class _WatermarkImageState extends State<_WatermarkImage> {
  File? _file;

  @override
  void initState() {
    super.initState();
    try {
      final result = widget.resolve(widget.watermark);
      if (result is Future<File?>) {
        unawaited(
          result
              .then((f) {
                if (mounted && f != null) setState(() => _file = f);
              })
              .catchError((Object _) {}),
        );
      } else {
        _file = result;
      }
    } catch (_) {
      // No watermark.
    }
  }

  @override
  Widget build(BuildContext context) {
    final file = _file;
    if (file == null) return const SizedBox.shrink();
    final w = widget.watermark;
    return IgnorePointer(
      child: ExcludeSemantics(
        child: LayoutBuilder(
          builder: (context, box) {
            final height = w.heightIn(box.maxHeight);
            if (!height.isFinite || height <= 0) return const SizedBox.shrink();
            return Align(
              key: const Key('watermark-overlay'),
              alignment: Alignment(
                w.position.isLeft ? -1 : 1,
                w.position.isTop ? -1 : 1,
              ),
              child: Padding(
                padding: EdgeInsets.all(context.ui(32)),
                child: ConstrainedBox(
                  constraints: BoxConstraints(
                    maxWidth: box.maxWidth * watermarkMaxWidthFraction,
                    maxHeight: height,
                  ),
                  child: Opacity(
                    key: const Key('watermark-opacity'),
                    opacity: w.opacity,
                    child: Image.file(
                      file,
                      key: const Key('watermark-image'),
                      height: height,
                      fit: BoxFit.contain,
                      gaplessPlayback: true,
                      errorBuilder: (context, error, stack) =>
                          const SizedBox.shrink(),
                    ),
                  ),
                ),
              ),
            );
          },
        ),
      ),
    );
  }
}
