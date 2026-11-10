// Tier-1 on-screen track picker (issue #3's stream selector, tier 1 — see
// AGENTS.md's "6-button baseline" and
// services/stream_selection_resolver.dart's TierOneOverride): the required
// 6-button-only path to pick, for the currently playing channel, which
// audio/subtitle track to use. Same non-setState convention as
// widgets/epg_overlay.dart: the parent (_ChannelPlayerPageState in
// main.dart) gates this behind a ValueNotifier<bool>
// (_trackPickerOpenNotifier) + ValueListenableBuilder — the video widgets
// underneath are never rebuilt by opening/closing this (see AGENTS.md's
// GL-texture gotcha for why that's load-bearing, not style).
//
// Navigation, same "row vs column" split already used by EpgOverlay
// (moveRow/moveColumn): raw Up/Down (bypassing InputAction, captured by
// the parent — they must NOT fall through to an actual channel switch while
// this is open) move focus within the currently active panel (Audio or
// Subtitle); raw Left/Right (bypassing InputAction, same 2D-grid exception
// already used for the EPG's column navigation) switch which panel is
// active — there are only 2 panels, so both directions just toggle. Select
// picks the focused option; Back closes without applying anything.
//
// `singlePanel` shows only `initialPanel` (the OSD's CC and audio buttons each
// open their own picker); Left/Right then do nothing. The two-panel layout is
// kept for the optional `trackSelector` accelerator.
//
// Touch: a tap on an option picks it (the same `onSelect` Select gives), a tap
// outside the panel closes the picker (the same `onClose` Back gives), and a
// phone floors each row at `OsdMetrics.minTapTarget`. The TV keeps its row
// height exactly, which is what its frozen goldens record.
import 'package:flutter/material.dart';

import '../ui/form_factor.dart';
import '../ui/osd_metrics.dart';
import '../ui/ui_scale.dart';

/// One selectable entry in a panel. `index` is the position in the SAME list
/// `getMediaInfo()` returned it from (`info.audio[index]`/
/// `info.subtitle[index]`) — the same indices `setAudioTracks`/
/// `setSubtitleTracks` and `TrackCandidate` (stream_selection_resolver.dart)
/// already use. `index == -1` only appears in the Subtitle panel: the
/// virtual "None" entry (explicitly disables subtitles).
class TrackOption {
  final int index;
  final String label;
  const TrackOption({required this.index, required this.label});
}

enum TrackSelectorPanel { audio, subtitle }

class TrackSelectorOverlay extends StatefulWidget {
  final List<TrackOption> audioOptions;
  final List<TrackOption> subtitleOptions;
  final int initialAudioFocusIndex;
  final int initialSubtitleFocusIndex;
  final TrackSelectorPanel initialPanel;
  final bool singlePanel;
  final void Function(TrackSelectorPanel panel, TrackOption option) onSelect;
  final VoidCallback onClose;

  const TrackSelectorOverlay({
    required Key key,
    required this.audioOptions,
    required this.subtitleOptions,
    this.initialAudioFocusIndex = 0,
    this.initialSubtitleFocusIndex = 0,
    this.initialPanel = TrackSelectorPanel.audio,
    this.singlePanel = false,
    required this.onSelect,
    required this.onClose,
  }) : super(key: key);

  @override
  State<TrackSelectorOverlay> createState() => TrackSelectorOverlayState();
}

class TrackSelectorOverlayState extends State<TrackSelectorOverlay> {
  late TrackSelectorPanel _activePanel;
  late int _focusedAudio;
  late int _focusedSubtitle;

  @override
  void initState() {
    super.initState();
    _activePanel = widget.initialPanel;
    _focusedAudio = widget.initialAudioFocusIndex.clamp(0, _lastIndex(widget.audioOptions));
    _focusedSubtitle = widget.initialSubtitleFocusIndex.clamp(0, _lastIndex(widget.subtitleOptions));
  }

  static int _lastIndex(List<TrackOption> options) => options.isEmpty ? 0 : options.length - 1;

  List<TrackOption> get _activeOptions =>
      _activePanel == TrackSelectorPanel.audio ? widget.audioOptions : widget.subtitleOptions;
  int get _focusedIndex => _activePanel == TrackSelectorPanel.audio ? _focusedAudio : _focusedSubtitle;

  /// Called by the parent on KeyDownEvent of raw arrowDown (delta 1) /
  /// arrowUp (delta -1) — moves focus within the active panel,
  /// without wrapping (same as EpgOverlay.moveRow).
  void moveFocus(int delta) {
    final options = _activeOptions;
    if (options.isEmpty) return;
    final next = (_focusedIndex + delta).clamp(0, options.length - 1);
    setState(() {
      if (_activePanel == TrackSelectorPanel.audio) {
        _focusedAudio = next;
      } else {
        _focusedSubtitle = next;
      }
    });
  }

  /// Called by the parent on raw arrowLeft/arrowRight KeyDownEvent — only 2
  /// panels exist, so either direction just toggles which one is active.
  /// No-op if the other panel has nothing to focus (e.g. no subtitle tracks
  /// at all — the "None" entry is always present, so this only happens if
  /// the caller passed an empty subtitleOptions, which the parent never
  /// does).
  void switchPanel() {
    if (widget.singlePanel) return;
    if (widget.audioOptions.isEmpty || widget.subtitleOptions.isEmpty) return;
    setState(() {
      _activePanel = _activePanel == TrackSelectorPanel.audio ? TrackSelectorPanel.subtitle : TrackSelectorPanel.audio;
    });
  }

  void selectFocused() {
    final options = _activeOptions;
    if (options.isEmpty || _focusedIndex >= options.length) return;
    widget.onSelect(_activePanel, options[_focusedIndex]);
  }

  /// A tap on an option: it becomes the active panel's focused one and is
  /// picked, which is exactly what walking to it and pressing Select does.
  void tapOption(TrackSelectorPanel panel, int index, List<TrackOption> options) {
    if (index < 0 || index >= options.length) return;
    setState(() {
      _activePanel = panel;
      if (panel == TrackSelectorPanel.audio) {
        _focusedAudio = index;
      } else {
        _focusedSubtitle = index;
      }
    });
    widget.onSelect(panel, options[index]);
  }

  // Pixels at 1080p scaled to this screen (the shared UiScale).
  double _u(double px) => context.ui(px);

  // The same, floored at a touch target where the family asks for one (0 on a
  // television, so its rows do not move).
  double _tap(double px) => context.uiTouch(px, min: OsdMetrics.of(context.formFactor).minTapTarget);

  Widget _buildPanel(String title, TrackSelectorPanel panel, List<TrackOption> options, int focusedIndex) {
    final isActive = _activePanel == panel;
    return Expanded(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(
            title,
            style: TextStyle(
              color: isActive ? Colors.white : Colors.white54,
              fontWeight: FontWeight.bold,
              fontSize: _u(20),
            ),
          ),
          SizedBox(height: _u(10)),
          if (options.isEmpty)
            Text(
              '(none available)',
              style: TextStyle(color: Colors.white38, fontSize: _u(18)),
            )
          else
            for (var i = 0; i < options.length; i++)
              GestureDetector(
                onTap: () => tapOption(panel, i, options),
                child: Container(
                key: Key('track-option-${panel.name}-${options[i].index}'),
                width: double.infinity,
                constraints: BoxConstraints(minHeight: _tap(36)),
                margin: EdgeInsets.symmetric(vertical: _u(3)),
                padding: EdgeInsets.symmetric(horizontal: _u(14), vertical: _u(9)),
                decoration: BoxDecoration(
                  color: (isActive && i == focusedIndex) ? Colors.white24 : Colors.transparent,
                  border: Border.all(
                    color: (isActive && i == focusedIndex) ? Colors.white : Colors.white12,
                    width: _u(1.5),
                  ),
                  borderRadius: BorderRadius.circular(_u(6)),
                ),
                alignment: Alignment.centerLeft,
                child: Text(
                  options[i].label,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(color: Colors.white, fontSize: _u(18)),
                ),
              ),
              ),
        ],
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Stack(
      key: const Key('track-selector-overlay'),
      children: [
        // Tapping the dim area closes the picker, the way Back does: on a phone
        // the only other way out would be the system Back gesture.
        Positioned.fill(
          child: GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTap: widget.onClose,
            child: Container(color: Colors.black.withValues(alpha: 0.75)),
          ),
        ),
        Center(
          child: ConstrainedBox(
            constraints: BoxConstraints(maxWidth: _u(720), maxHeight: _u(560)),
            child: Container(
              padding: EdgeInsets.all(_u(28)),
              decoration: BoxDecoration(
                color: Colors.grey.shade900,
                borderRadius: BorderRadius.circular(_u(16)),
                border: Border.all(color: Colors.white24),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                mainAxisSize: MainAxisSize.min,
                children: [
                  Text(
                    widget.singlePanel
                        ? (widget.initialPanel == TrackSelectorPanel.audio ? 'Audio' : 'Subtitles')
                        : 'Track selector',
                    style: TextStyle(color: Colors.white, fontSize: _u(26), fontWeight: FontWeight.bold),
                  ),
                  SizedBox(height: _u(20)),
                  Row(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      if (!widget.singlePanel || widget.initialPanel == TrackSelectorPanel.audio)
                        _buildPanel('Audio', TrackSelectorPanel.audio, widget.audioOptions, _focusedAudio),
                      if (!widget.singlePanel) SizedBox(width: _u(32)),
                      if (!widget.singlePanel || widget.initialPanel == TrackSelectorPanel.subtitle)
                        _buildPanel('Subtitle', TrackSelectorPanel.subtitle, widget.subtitleOptions, _focusedSubtitle),
                    ],
                  ),
                ],
              ),
            ),
          ),
        ),
      ],
    );
  }
}
