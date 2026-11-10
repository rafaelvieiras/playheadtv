// The mosaic on a phone: one column instead of a ring.
//
// The frame (`mosaic_frame.dart`) is a 4x4 ring around a central preview, and
// that shape is the point on a television — a satellite waiting screen read from
// three metres. On a 360 dp handheld the same frame fills 26 % of the screen and
// leaves the rest black, and a portrait phone would waste three quarters of its
// height the same way. A column is aspect-agnostic, holds every channel instead
// of twelve, and reads like every other list a phone owner already uses.
//
// Presentational only, exactly like the frame: the page owns the data, the
// preview controller and the focused index, and hands in the featured channel's
// picture as a ready widget (never rebuilt here, so the preview's texture is
// only ever re-laid-out — the GL-texture gotcha in AGENTS.md).
//
// Focus is the page's, not the list's: every row is built eagerly (a phone list
// of channels is short, and a row that is not built cannot hold focus at all),
// and a move scrolls the focused row into view instead of relying on traversal.
import 'package:flutter/material.dart';

import '../ui/ui_scale.dart';
import 'app_icons.dart';
import 'mosaic_frame.dart';

class PhoneMosaicView extends StatefulWidget {
  /// Tile 0 is the featured one; the rest follow in order; Settings is last.
  final List<MosaicTileSpec> tiles;
  final List<FocusNode> focusNodes;
  final int focusedIndex;

  /// The featured tile's picture (the preview video, or its art/placeholder).
  final Widget featuredBackground;

  /// A tap on a row: the page focuses it and opens it, the same outcome Select
  /// on that row has.
  final void Function(int index) onSelect;

  const PhoneMosaicView({
    super.key,
    required this.tiles,
    required this.focusNodes,
    required this.focusedIndex,
    required this.featuredBackground,
    required this.onSelect,
  });

  static const Key listKey = Key('mosaic-list');

  /// The featured card never takes more than this much of the height, so the
  /// rows below it (the thing a phone is for) always have room: a landscape
  /// phone is 360 dp tall and a 16:9 card of the full width would be 438.
  static const double featuredMaxHeightFraction = 0.4;

  @override
  State<PhoneMosaicView> createState() => _PhoneMosaicViewState();
}

class _PhoneMosaicViewState extends State<PhoneMosaicView> {
  late List<GlobalKey> _rowKeys = _keysFor(widget.tiles.length);

  static List<GlobalKey> _keysFor(int n) => List.generate(n, (_) => GlobalKey());

  @override
  void didUpdateWidget(PhoneMosaicView old) {
    super.didUpdateWidget(old);
    if (old.tiles.length != widget.tiles.length) {
      _rowKeys = _keysFor(widget.tiles.length);
    }
    if (old.focusedIndex != widget.focusedIndex) _revealFocused();
  }

  // The page moves the focus with the arrows; the list follows it. Without this
  // a move past the bottom of the screen would move an off-screen focus ring.
  void _revealFocused() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      final index = widget.focusedIndex;
      if (index < 0 || index >= _rowKeys.length) return;
      final ctx = _rowKeys[index].currentContext;
      if (ctx == null) return;
      Scrollable.ensureVisible(ctx, alignment: 0.5, duration: const Duration(milliseconds: 150));
    });
  }

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        // 16:9 of the width, but never more than 40% of the height: on a
        // landscape phone the width is the larger number and the card has to
        // leave room for the rows.
        final featuredHeight = (constraints.maxHeight * PhoneMosaicView.featuredMaxHeightFraction)
            .clamp(context.ui(56), constraints.maxWidth * 9 / 16);
        return SingleChildScrollView(
          key: PhoneMosaicView.listKey,
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              SizedBox(
                height: featuredHeight.toDouble(),
                child: KeyedSubtree(key: _rowKeys[0], child: _featured(context)),
              ),
              for (var i = 1; i < widget.tiles.length; i++)
                KeyedSubtree(key: _rowKeys[i], child: _row(context, i)),
            ],
          ),
        );
      },
    );
  }

  Widget _featured(BuildContext context) {
    final t = widget.tiles[0];
    final focused = widget.focusedIndex == 0;
    return Focus(
      focusNode: widget.focusNodes[0],
      child: GestureDetector(
        onTap: () => widget.onSelect(0),
        child: Container(
          key: t.key,
          clipBehavior: Clip.antiAlias,
          decoration: _decoration(context, focused),
          child: Stack(
            fit: StackFit.expand,
            children: [
              // Positioned.fill, never the bare widget: a Stack child without
              // Positioned breaks the fvp/libmdk GL texture composition on some
              // driver combinations (see main.dart's EPG comment).
              Positioned.fill(child: widget.featuredBackground),
              Positioned(
                left: context.ui(12),
                right: context.ui(12),
                bottom: context.ui(12),
                child: Row(
                  children: [
                    Flexible(
                      child: Container(
                        padding: EdgeInsets.symmetric(horizontal: context.ui(10), vertical: context.ui(4)),
                        color: Colors.black54,
                        child: Text(
                          t.isSettings ? 'Settings' : '${t.number} — ${t.name}',
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: TextStyle(
                            color: Colors.white,
                            fontSize: context.ui(22),
                            fontWeight: FontWeight.bold,
                          ),
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _row(BuildContext context, int index) {
    final t = widget.tiles[index];
    final focused = widget.focusedIndex == index;
    final numberWidth = context.ui(48);
    return Focus(
      focusNode: widget.focusNodes[index],
      child: GestureDetector(
        onTap: () => widget.onSelect(index),
        child: Container(
          key: t.key,
          constraints: BoxConstraints(minHeight: context.ui(56)),
          margin: EdgeInsets.only(top: context.ui(6)),
          padding: EdgeInsets.symmetric(horizontal: context.ui(12), vertical: context.ui(8)),
          decoration: _decoration(context, focused, color: Colors.grey.shade900),
          child: Row(
            children: [
              SizedBox(
                width: numberWidth,
                child: Text(
                  t.isSettings ? '' : '${t.number}',
                  style: TextStyle(color: Colors.white70, fontSize: context.ui(20), fontWeight: FontWeight.bold),
                ),
              ),
              if (t.isSettings) Icon(AppIcons.settings, color: Colors.white, size: context.ui(26)),
              SizedBox(width: context.ui(8)),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    Text(
                      t.isSettings ? 'Settings' : t.name,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(color: Colors.white, fontSize: context.ui(20)),
                    ),
                    if (t.nowTitle != null)
                      Text(
                        t.nowTitle!,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(color: Colors.white60, fontSize: context.ui(16)),
                      ),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  BoxDecoration _decoration(BuildContext context, bool focused, {Color? color}) => BoxDecoration(
        color: color,
        border: Border.all(
          color: focused ? Colors.white : Colors.white24,
          width: context.ui(focused ? 4 : 2),
        ),
        borderRadius: BorderRadius.circular(context.ui(10)),
      );
}
