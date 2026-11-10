// The channel OSD's design units, per layout family.
//
// Every number here is "pixels at 1080p", the same unit `context.ui(px)` reads,
// so the television values are exactly the literals this widget used before the
// phone existed (`OsdMetrics.tv` is pinned by a unit test and by the goldens,
// which changed by not one byte). The phone differs for two reasons that have
// nothing to do with taste: its panel has to leave the picture visible, and its
// controls have to be reachable with a finger — so it drops the two rows that
// only exist for a remote (the transport row, every button of which is disabled
// in every build, and the channel card strip) and it puts a 48 dp floor under
// anything tappable, through `context.uiTouch(px, min: m.minTapTarget)`.
//
// See docs/PHONE_UI_PLAN.md (slices 2 and 7) and docs/ARCHITECTURE.md "Client
// form factors".
import 'form_factor.dart';

class OsdMetrics {
  // Chip (the HD / 1080p / STEREO badges next to the title).
  final double chipMarginLeft;
  final double chipPaddingH;
  final double chipPaddingV;
  final double chipRadius;
  final double chipFontSize;

  // A control button: an icon, optionally with a label.
  final double buttonPaddingH;
  final double buttonPaddingHLabel;
  final double buttonPaddingV;
  final double buttonBorder;
  final double buttonRadius;
  final double buttonIconSize;
  final double buttonGap;
  final double buttonLabelSize;

  /// The smallest box a finger may land on, in dp. Zero on a television, which
  /// is measured from across a room instead of touched.
  final double minTapTarget;

  // A recent-channel card in the OSD's "Channels" strip.
  final double cardWidth;
  final double cardMarginRight;
  final double cardPadding;
  final double cardBorder;
  final double cardRadius;
  final double cardThumbSize;
  final double cardGap;
  final double cardNumberSize;
  final double cardNameSize;
  final double cardIconSize;
  final bool recentChannelCards;

  // The leading channel badge and the channel identity in the header.
  final double logoSize;
  final double logoIconSize;
  final double nameFontSize;
  final double nameLetterSpacing;
  final double inlineLogoHeight;
  final double inlineLogoMaxWidth;

  // The clock and the "Ends HH:MM" line under it.
  final double clockTop;
  final double clockRight;
  final double clockFontSize;
  final double endsAtFontSize;

  // The bottom panel.
  final double panelLeft;
  final double panelTop;
  final double panelRight;
  final double panelBottom;
  final double titleFontSize;
  final double titleGap;
  final double metaFontSize;
  final double headerGap;

  /// The transport row (play/pause, skip, rewind, fast-forward, next). Laid out
  /// on a television for a follow-up that has not happened: every button in it
  /// is disabled. A phone does not carry the row at all.
  final bool transportRow;
  final double transportGap;

  // Progress bar and elapsed time.
  final double progressRadius;
  final double progressHeight;
  final double elapsedGap;
  final double elapsedFontSize;
  final double controlsGap;

  // The "Up next" line.
  final double upNextFontSize;
  final double upNextGap;

  // The "Channels" section under the controls.
  final double channelsGap;
  final double channelsTitleFontSize;
  final double channelsTitleGap;

  const OsdMetrics({
    required this.chipMarginLeft,
    required this.chipPaddingH,
    required this.chipPaddingV,
    required this.chipRadius,
    required this.chipFontSize,
    required this.buttonPaddingH,
    required this.buttonPaddingHLabel,
    required this.buttonPaddingV,
    required this.buttonBorder,
    required this.buttonRadius,
    required this.buttonIconSize,
    required this.buttonGap,
    required this.buttonLabelSize,
    required this.minTapTarget,
    required this.cardWidth,
    required this.cardMarginRight,
    required this.cardPadding,
    required this.cardBorder,
    required this.cardRadius,
    required this.cardThumbSize,
    required this.cardGap,
    required this.cardNumberSize,
    required this.cardNameSize,
    required this.cardIconSize,
    required this.recentChannelCards,
    required this.logoSize,
    required this.logoIconSize,
    required this.nameFontSize,
    required this.nameLetterSpacing,
    required this.inlineLogoHeight,
    required this.inlineLogoMaxWidth,
    required this.clockTop,
    required this.clockRight,
    required this.clockFontSize,
    required this.endsAtFontSize,
    required this.panelLeft,
    required this.panelTop,
    required this.panelRight,
    required this.panelBottom,
    required this.titleFontSize,
    required this.titleGap,
    required this.metaFontSize,
    required this.headerGap,
    required this.transportRow,
    required this.transportGap,
    required this.progressRadius,
    required this.progressHeight,
    required this.elapsedGap,
    required this.elapsedFontSize,
    required this.controlsGap,
    required this.upNextFontSize,
    required this.upNextGap,
    required this.channelsGap,
    required this.channelsTitleFontSize,
    required this.channelsTitleGap,
  });

  static OsdMetrics of(ClientFormFactor formFactor) =>
      formFactor == ClientFormFactor.phone ? phone : tv;

  /// The 10-foot layout, unchanged since before the phone existed.
  static const OsdMetrics tv = OsdMetrics(
    chipMarginLeft: 8,
    chipPaddingH: 6,
    chipPaddingV: 1,
    chipRadius: 3,
    chipFontSize: 11,
    buttonPaddingH: 10,
    buttonPaddingHLabel: 12,
    buttonPaddingV: 8,
    buttonBorder: 2,
    buttonRadius: 8,
    buttonIconSize: 28,
    buttonGap: 8,
    buttonLabelSize: 16,
    minTapTarget: 0,
    cardWidth: 170,
    cardMarginRight: 12,
    cardPadding: 8,
    cardBorder: 2,
    cardRadius: 8,
    cardThumbSize: 36,
    cardGap: 8,
    cardNumberSize: 14,
    cardNameSize: 12,
    cardIconSize: 20,
    recentChannelCards: true,
    logoSize: 64,
    logoIconSize: 32,
    nameFontSize: 15,
    nameLetterSpacing: 0.5,
    inlineLogoHeight: 26,
    inlineLogoMaxWidth: 220,
    clockTop: 24,
    clockRight: 32,
    clockFontSize: 28,
    endsAtFontSize: 14,
    panelLeft: 32,
    panelTop: 48,
    panelRight: 32,
    panelBottom: 20,
    titleFontSize: 26,
    titleGap: 4,
    metaFontSize: 15,
    headerGap: 8,
    transportRow: true,
    transportGap: 4,
    progressRadius: 4,
    progressHeight: 6,
    elapsedGap: 12,
    elapsedFontSize: 13,
    controlsGap: 8,
    upNextFontSize: 13,
    upNextGap: 16,
    channelsGap: 12,
    channelsTitleFontSize: 16,
    channelsTitleGap: 8,
  );

  /// The handheld layout: the same information, without the rows a phone has no
  /// use for and with every control at a touch target. The type is a little
  /// larger than the television's in design units because the phone's scale is
  /// around 0.86 instead of 1.0.
  static const OsdMetrics phone = OsdMetrics(
    chipMarginLeft: 8,
    chipPaddingH: 6,
    chipPaddingV: 2,
    chipRadius: 3,
    chipFontSize: 15,
    buttonPaddingH: 12,
    buttonPaddingHLabel: 14,
    buttonPaddingV: 6,
    buttonBorder: 2,
    buttonRadius: 8,
    buttonIconSize: 26,
    buttonGap: 8,
    buttonLabelSize: 17,
    minTapTarget: 48,
    cardWidth: 170,
    cardMarginRight: 12,
    cardPadding: 8,
    cardBorder: 2,
    cardRadius: 8,
    cardThumbSize: 36,
    cardGap: 8,
    cardNumberSize: 14,
    cardNameSize: 14,
    cardIconSize: 20,
    recentChannelCards: false,
    logoSize: 52,
    logoIconSize: 28,
    nameFontSize: 16,
    nameLetterSpacing: 0.5,
    inlineLogoHeight: 24,
    inlineLogoMaxWidth: 150,
    clockTop: 16,
    clockRight: 16,
    clockFontSize: 26,
    endsAtFontSize: 16,
    panelLeft: 16,
    panelTop: 28,
    panelRight: 16,
    panelBottom: 14,
    titleFontSize: 26,
    titleGap: 3,
    metaFontSize: 16,
    headerGap: 6,
    transportRow: false,
    transportGap: 0,
    progressRadius: 3,
    progressHeight: 5,
    elapsedGap: 10,
    elapsedFontSize: 15,
    controlsGap: 8,
    upNextFontSize: 15,
    upNextGap: 10,
    channelsGap: 10,
    channelsTitleFontSize: 17,
    channelsTitleGap: 8,
  );
}
