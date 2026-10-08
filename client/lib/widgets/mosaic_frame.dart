// The mosaic's frame: 4x4 grid, the featured video in the central 2x2 block and
// the other tiles around it (see mosaic_nav.dart for the cell order). Purely
// presentational: the page owns the data, the focus and the preview
// controller and hands in the featured tile's background as a ready widget.
//
// Geometry comes from `computeMosaicLayout` off the LayoutBuilder constraints
// (the frame fills the height); text, icons and focus rings scale with the
// app-wide UiScale. Nothing here calls setState, and the featured background
// keeps its place in the tree across resizes, so the preview's texture is only
// ever re-laid-out, never recreated (AGENTS.md, GL-texture gotcha).
import 'package:flutter/material.dart';

import '../mosaic_layout.dart';
import '../mosaic_nav.dart';
import '../ui/ui_scale.dart';
import 'app_icons.dart';
import 'art_image.dart';

/// What the frame needs to draw one tile.
class MosaicTileSpec {
  /// Key of the tile's container (tests find a tile by it).
  final Key key;
  final bool isSettings;
  final int number;
  final String name;
  final String? artUrl;

  const MosaicTileSpec.channel({required this.key, required this.number, required this.name, this.artUrl})
      : isSettings = false;
  const MosaicTileSpec.settings()
      : key = const Key('mosaic-tile-settings'),
        isSettings = true,
        number = 0,
        name = '',
        artUrl = null;
}

class MosaicFrameView extends StatelessWidget {
  /// Tile 0 is the featured one; the rest fill `mosaicRingSlots` in order.
  final List<MosaicTileSpec> tiles;
  final List<FocusNode> focusNodes;
  final int focusedIndex;

  /// The featured tile's picture (the preview video, or its art/placeholder).
  final Widget featuredBackground;
  final double safeMargin;

  const MosaicFrameView({
    super.key,
    required this.tiles,
    required this.focusNodes,
    required this.focusedIndex,
    required this.featuredBackground,
    this.safeMargin = defaultMosaicSafeMargin,
  });

  static const Key frameKey = Key('mosaic-frame');

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(builder: (context, constraints) {
      final layout = computeMosaicLayout(constraints.biggest, safeMargin: safeMargin);
      if (layout.isEmpty) return const SizedBox.shrink();
      final ringCount = tiles.length - 1;
      return Stack(
        children: [
          Positioned.fromRect(
            rect: layout.frame,
            child: Stack(
              key: frameKey,
              children: [
                for (var i = 0; i < ringCount && i < mosaicRingSlots.length; i++)
                  Positioned.fromRect(
                    rect: layout.cell(mosaicRingSlots[i].row, mosaicRingSlots[i].col),
                    child: _smallTile(context, i + 1),
                  ),
                Positioned.fromRect(rect: layout.featured, child: _featuredTile(context)),
              ],
            ),
          ),
        ],
      );
    });
  }

  BoxDecoration _decoration(BuildContext context, bool focused, {Color? color}) => BoxDecoration(
        color: color,
        border: Border.all(
          color: focused ? Colors.white : Colors.white24,
          width: context.ui(focused ? 5 : 2.5),
        ),
        borderRadius: BorderRadius.circular(context.ui(10)),
      );

  Widget _featuredTile(BuildContext context) {
    final t = tiles[0];
    final focused = focusedIndex == 0;
    return Focus(
      focusNode: focusNodes[0],
      child: Container(
        key: t.key,
        width: double.infinity,
        height: double.infinity,
        clipBehavior: Clip.antiAlias,
        decoration: _decoration(context, focused),
        child: Stack(
          fit: StackFit.expand,
          children: [
            // Positioned.fill (not the bare widget): a Stack child without
            // Positioned breaks the fvp/libmdk GL texture composition on some
            // GPU/driver combinations — see the comment on the EPG's
            // ValueListenableBuilder in main.dart.
            Positioned.fill(child: featuredBackground),
            Positioned(
              left: context.ui(14),
              right: context.ui(14),
              bottom: context.ui(14),
              child: Row(
                children: [
                  Flexible(
                    child: Container(
                      padding: EdgeInsets.symmetric(horizontal: context.ui(12), vertical: context.ui(5)),
                      color: Colors.black54,
                      child: Text(
                        '${t.number} — ${t.name}',
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(color: Colors.white, fontSize: context.ui(28), fontWeight: FontWeight.bold),
                      ),
                    ),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _smallTile(BuildContext context, int idx) {
    final t = tiles[idx];
    final focused = focusedIndex == idx;
    if (t.isSettings) {
      return Focus(
        focusNode: focusNodes[idx],
        child: Container(
          key: t.key,
          width: double.infinity,
          height: double.infinity,
          alignment: Alignment.center,
          decoration: _decoration(context, focused, color: Colors.grey.shade900),
          child: FittedBox(
            fit: BoxFit.scaleDown,
            child: Padding(
              padding: EdgeInsets.all(context.ui(6)),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(AppIcons.settings, color: Colors.white, size: context.ui(48)),
                  SizedBox(height: context.ui(6)),
                  Text('Settings', style: TextStyle(color: Colors.white, fontSize: context.ui(22))),
                ],
              ),
            ),
          ),
        ),
      );
    }
    return Focus(
      focusNode: focusNodes[idx],
      child: Container(
        key: t.key,
        width: double.infinity,
        height: double.infinity,
        clipBehavior: Clip.antiAlias,
        decoration: _decoration(context, focused),
        child: MosaicTileArt(spec: t),
      ),
    );
  }
}

/// A channel tile's picture: its art, or the number and name as a placeholder.
class MosaicTileArt extends StatelessWidget {
  final MosaicTileSpec spec;
  const MosaicTileArt({super.key, required this.spec});

  Widget _fallback(BuildContext context) => Container(
        color: Colors.grey.shade900,
        alignment: Alignment.center,
        padding: EdgeInsets.all(context.ui(6)),
        child: Text(
          '${spec.number}\n${spec.name}',
          textAlign: TextAlign.center,
          maxLines: 3,
          overflow: TextOverflow.ellipsis,
          style: TextStyle(color: Colors.white, fontSize: context.ui(26)),
        ),
      );

  @override
  Widget build(BuildContext context) {
    final url = spec.artUrl;
    return url != null ? ArtImage(url, fallback: _fallback) : _fallback(context);
  }
}
