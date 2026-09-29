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
// (moveRow/moveColumn): Up/Down (channelNext/channelPrevious, captured by
// the parent — they must NOT fall through to an actual channel switch while
// this is open) move focus within the currently active panel (Audio or
// Subtitle); raw Left/Right (bypassing InputAction, same 2D-grid exception
// already used for the EPG's column navigation) switch which panel is
// active — there are only 2 panels, so both directions just toggle. Select
// picks the focused option; Back closes without applying anything.
import 'package:flutter/material.dart';

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
  final void Function(TrackSelectorPanel panel, TrackOption option) onSelect;
  final VoidCallback onClose;

  const TrackSelectorOverlay({
    required Key key,
    required this.audioOptions,
    required this.subtitleOptions,
    this.initialAudioFocusIndex = 0,
    this.initialSubtitleFocusIndex = 0,
    required this.onSelect,
    required this.onClose,
  }) : super(key: key);

  @override
  State<TrackSelectorOverlay> createState() => TrackSelectorOverlayState();
}

class TrackSelectorOverlayState extends State<TrackSelectorOverlay> {
  TrackSelectorPanel _activePanel = TrackSelectorPanel.audio;
  late int _focusedAudio;
  late int _focusedSubtitle;

  @override
  void initState() {
    super.initState();
    _focusedAudio = widget.initialAudioFocusIndex.clamp(0, _lastIndex(widget.audioOptions));
    _focusedSubtitle = widget.initialSubtitleFocusIndex.clamp(0, _lastIndex(widget.subtitleOptions));
  }

  static int _lastIndex(List<TrackOption> options) => options.isEmpty ? 0 : options.length - 1;

  List<TrackOption> get _activeOptions =>
      _activePanel == TrackSelectorPanel.audio ? widget.audioOptions : widget.subtitleOptions;
  int get _focusedIndex => _activePanel == TrackSelectorPanel.audio ? _focusedAudio : _focusedSubtitle;

  /// Called by the parent on KeyDownEvent of channelNext (delta 1) /
  /// channelPrevious (delta -1) — moves focus within the active panel,
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
    if (widget.audioOptions.isEmpty || widget.subtitleOptions.isEmpty) return;
    setState(() {
      _activePanel =
          _activePanel == TrackSelectorPanel.audio ? TrackSelectorPanel.subtitle : TrackSelectorPanel.audio;
    });
  }

  void selectFocused() {
    final options = _activeOptions;
    if (options.isEmpty || _focusedIndex >= options.length) return;
    widget.onSelect(_activePanel, options[_focusedIndex]);
  }

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
              fontSize: 14,
            ),
          ),
          const SizedBox(height: 8),
          if (options.isEmpty)
            const Text('(none available)', style: TextStyle(color: Colors.white38, fontSize: 13))
          else
            for (var i = 0; i < options.length; i++)
              Container(
                key: Key('track-option-${panel.name}-${options[i].index}'),
                width: double.infinity,
                margin: const EdgeInsets.symmetric(vertical: 2),
                padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
                decoration: BoxDecoration(
                  color: (isActive && i == focusedIndex) ? Colors.white24 : Colors.transparent,
                  border: Border.all(color: (isActive && i == focusedIndex) ? Colors.white : Colors.white12),
                  borderRadius: BorderRadius.circular(4),
                ),
                child: Text(
                  options[i].label,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: const TextStyle(color: Colors.white, fontSize: 13),
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
        Positioned.fill(child: Container(color: Colors.black.withValues(alpha: 0.75))),
        Center(
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 520, maxHeight: 420),
            child: Container(
              padding: const EdgeInsets.all(20),
              decoration: BoxDecoration(
                color: Colors.grey.shade900,
                borderRadius: BorderRadius.circular(12),
                border: Border.all(color: Colors.white24),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                mainAxisSize: MainAxisSize.min,
                children: [
                  const Text(
                    'Track selector',
                    style: TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.bold),
                  ),
                  const SizedBox(height: 16),
                  Row(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      _buildPanel('Audio', TrackSelectorPanel.audio, widget.audioOptions, _focusedAudio),
                      const SizedBox(width: 24),
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
