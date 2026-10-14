// The watermark the guide attaches to an entry (docs/ARCHITECTURE.md
// "Watermark"): the backend resolves channel/block settings per entry, the
// client only draws what the entry on air carries. Pure Dart so the parsing
// and the layout math are unit-testable without a widget.
/// The four corners a watermark can sit in.
enum WatermarkPosition {
  topLeft('top_left'),
  topRight('top_right'),
  bottomLeft('bottom_left'),
  bottomRight('bottom_right');

  final String wire;
  const WatermarkPosition(this.wire);

  static WatermarkPosition? fromWire(Object? value) {
    for (final p in values) {
      if (p.wire == value) return p;
    }
    return null;
  }

  bool get isTop => this == topLeft || this == topRight;
  bool get isLeft => this == topLeft || this == bottomLeft;
}

/// Bounds of the options, the backend's (domain.WatermarkMinSize/MaxSize).
const watermarkMinSizePct = 2;
const watermarkMaxSizePct = 40;
const watermarkDefaultOpacityPct = 70;
const watermarkDefaultSizePct = 8;

/// The widest a watermark may be, as a fraction of the area it is drawn in: a
/// wide logo at a large size must not cover the picture.
const watermarkMaxWidthFraction = 0.4;

class EpgWatermark {
  /// Names the stored image; the bytes are `GET /watermarks/{imageId}/image`.
  final String imageId;
  final WatermarkPosition position;
  final int opacityPct; // 0-100
  final int sizePct; // percent of the video height

  const EpgWatermark({
    required this.imageId,
    this.position = WatermarkPosition.bottomRight,
    this.opacityPct = watermarkDefaultOpacityPct,
    this.sizePct = watermarkDefaultSizePct,
  });

  /// Reads the entry's `watermark` object. Anything unusable (not an object,
  /// no image id) is null, i.e. no watermark: a bad value never becomes an
  /// error. Out-of-range options are clamped, an unknown corner is the
  /// default one.
  static EpgWatermark? tryParse(Object? json) {
    if (json is! Map) return null;
    final id = json['image_id'];
    if (id is! String || id.isEmpty) return null;
    final opacity = json['opacity_pct'];
    final size = json['size_pct'];
    return EpgWatermark(
      imageId: id,
      position: WatermarkPosition.fromWire(json['position']) ?? WatermarkPosition.bottomRight,
      opacityPct: (opacity is num ? opacity.round() : watermarkDefaultOpacityPct).clamp(0, 100),
      sizePct: (size is num ? size.round() : watermarkDefaultSizePct).clamp(watermarkMinSizePct, watermarkMaxSizePct),
    );
  }

  Map<String, dynamic> toJson() => {
        'image_id': imageId,
        'position': position.wire,
        'opacity_pct': opacityPct,
        'size_pct': sizePct,
      };

  /// 0.0-1.0.
  double get opacity => opacityPct / 100;

  /// The height the image is drawn at in an area [areaHeight] tall.
  double heightIn(double areaHeight) => areaHeight * sizePct / 100;

  @override
  bool operator ==(Object other) =>
      other is EpgWatermark &&
      other.imageId == imageId &&
      other.position == position &&
      other.opacityPct == opacityPct &&
      other.sizePct == sizePct;

  @override
  int get hashCode => Object.hash(imageId, position, opacityPct, sizePct);
}
