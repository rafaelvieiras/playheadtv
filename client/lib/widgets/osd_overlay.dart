// Channel OSD: the info bar shown over the video — channel logo, program
// title, `number · name · start–end`, stream chips, the transport control
// row, a progress bar, the CC / audio-track buttons and the "Channels" entry
// to the EPG. Clock (local time + when the current program ends) sits in the
// top-right corner.
//
// Same non-setState convention as widgets/epg_overlay.dart: the parent
// (_ChannelPlayerPageState in main.dart) shows/hides it through a
// ValueNotifier<bool> + ValueListenableBuilder, never setState — see
// AGENTS.md's GL-texture gotcha. This widget only owns its own ticker (the
// progress bar and clock) and its own focus state.
//
// Focus mode (6-button baseline): while the OSD is merely displayed (auto-show
// at channel start/switch) it is passive and Up/Down still switch channel.
// Select turns on `focusMode`; then the parent relays the raw arrows to
// `move` and Select to `activate`, and the focused control is outlined.
// Controls that do nothing yet (play/pause, previous/next, rewind/fast-
// forward) are rendered dimmed and skipped by focus.
import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:video_player/video_player.dart';
import 'package:fvp/fvp.dart'; // getMediaInfo() on VideoPlayerController

import 'app_icons.dart';

enum _OsdItem { captions, audio, channels }

class ChannelOsd extends StatefulWidget {
  final int channelNumber;
  final String channelName;
  final String? channelArtUrl;
  final String itemLabel;
  final String? nextItemLabel;
  final DateTime startAt;
  final DateTime endAt;
  final VideoPlayerController? controller;
  final ValueListenable<bool> focusMode;
  final VoidCallback onOpenSubtitles;
  final VoidCallback onOpenAudio;
  final VoidCallback onOpenChannels;

  const ChannelOsd({
    required Key key,
    required this.channelNumber,
    required this.channelName,
    required this.channelArtUrl,
    required this.itemLabel,
    required this.nextItemLabel,
    required this.startAt,
    required this.endAt,
    required this.controller,
    required this.focusMode,
    required this.onOpenSubtitles,
    required this.onOpenAudio,
    required this.onOpenChannels,
  }) : super(key: key);

  @override
  State<ChannelOsd> createState() => ChannelOsdState();
}

class ChannelOsdState extends State<ChannelOsd> {
  Timer? _tickTimer;
  _OsdItem? _focused;

  @override
  void initState() {
    super.initState();
    _tickTimer = Timer.periodic(const Duration(milliseconds: 500), (_) {
      if (mounted) setState(() {});
    });
    widget.focusMode.addListener(_onFocusModeChanged);
  }

  @override
  void didUpdateWidget(ChannelOsd oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.focusMode != widget.focusMode) {
      oldWidget.focusMode.removeListener(_onFocusModeChanged);
      widget.focusMode.addListener(_onFocusModeChanged);
    }
  }

  @override
  void dispose() {
    widget.focusMode.removeListener(_onFocusModeChanged);
    _tickTimer?.cancel();
    super.dispose();
  }

  // Entering focus mode starts on the first available control; leaving it
  // clears the outline.
  void _onFocusModeChanged() {
    setState(() => _focused = widget.focusMode.value ? _rows().firstOrNull?.firstOrNull : null);
  }

  // Focusable rows, top to bottom: [CC, audio] then [Channels]. CC needs a
  // real subtitle track and audio needs a real choice (>1 track).
  List<List<_OsdItem>> _rows() {
    final info = widget.controller?.getMediaInfo();
    final hasSubtitles = (info?.subtitle ?? const []).isNotEmpty;
    final hasAudioChoice = (info?.audio ?? const []).length > 1;
    return [
      [if (hasSubtitles) _OsdItem.captions, if (hasAudioChoice) _OsdItem.audio],
      [_OsdItem.channels],
    ].where((r) => r.isNotEmpty).toList();
  }

  /// Relayed by the parent from raw arrow keys while in focus mode.
  void move(int dx, int dy) {
    final rows = _rows();
    if (rows.isEmpty) return;
    var row = rows.indexWhere((r) => r.contains(_focused));
    if (row < 0) row = 0;
    var col = rows[row].indexOf(_focused ?? rows[row].first);
    if (col < 0) col = 0;
    if (dy != 0) {
      row = (row + dy).clamp(0, rows.length - 1);
      col = col.clamp(0, rows[row].length - 1);
    } else {
      col = (col + dx).clamp(0, rows[row].length - 1);
    }
    setState(() => _focused = rows[row][col]);
  }

  /// Relayed by the parent on Select while in focus mode.
  void activateFocused() {
    switch (_focused) {
      case _OsdItem.captions:
        widget.onOpenSubtitles();
      case _OsdItem.audio:
        widget.onOpenAudio();
      case _OsdItem.channels:
        widget.onOpenChannels();
      case null:
        break;
    }
  }

  static String _hhmm(DateTime dt) {
    final l = dt.toLocal();
    return '${l.hour.toString().padLeft(2, '0')}:${l.minute.toString().padLeft(2, '0')}';
  }

  static String _fmtDuration(Duration d) {
    final h = d.inHours;
    final m = d.inMinutes.remainder(60).toString().padLeft(2, '0');
    final s = d.inSeconds.remainder(60).toString().padLeft(2, '0');
    return h > 0 ? '$h:$m:$s' : '${d.inMinutes}:$s';
  }

  List<String> _chips() {
    final info = widget.controller?.getMediaInfo();
    final chips = <String>[];
    final video = (info?.video ?? const []).firstOrNull;
    if (video != null) {
      final h = video.codec.height;
      if (h >= 720) chips.add('HD');
      if (h >= 2160) {
        chips.add('4K');
      } else if (h > 0) {
        chips.add('${h}p');
      }
    }
    final audio = (info?.audio ?? const []).firstOrNull;
    if (audio != null) {
      chips.add(switch (audio.codec.channels) {
        1 => 'MONO',
        2 => 'STEREO',
        6 => '5.1',
        8 => '7.1',
        final n => '${n}CH',
      });
    }
    return chips;
  }

  Widget _chip(String text) => Container(
        margin: const EdgeInsets.only(left: 8),
        padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
        decoration: BoxDecoration(
          color: Colors.white24,
          borderRadius: BorderRadius.circular(3),
        ),
        child: Text(text, style: const TextStyle(color: Colors.white, fontSize: 11, fontWeight: FontWeight.bold)),
      );

  Widget _button(Key key, IconData icon, {String? label, bool enabled = true, bool focused = false}) {
    return Opacity(
      opacity: enabled ? 1 : 0.3,
      child: Container(
        key: key,
        padding: EdgeInsets.symmetric(horizontal: label == null ? 10 : 12, vertical: 8),
        decoration: BoxDecoration(
          color: focused ? Colors.white24 : Colors.transparent,
          border: Border.all(color: focused ? Colors.white : Colors.transparent, width: 2),
          borderRadius: BorderRadius.circular(8),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, color: Colors.white, size: 28),
            if (label != null) ...[
              const SizedBox(width: 8),
              Text(label, style: const TextStyle(color: Colors.white, fontSize: 16)),
            ],
          ],
        ),
      ),
    );
  }

  Widget _logo() {
    Widget fallback() => const Icon(AppIcons.channels, color: Colors.white, size: 32);
    final url = widget.channelArtUrl;
    return Container(
      key: const Key('osd-channel-logo'),
      width: 64,
      height: 64,
      decoration: const BoxDecoration(color: Colors.white12, shape: BoxShape.circle),
      clipBehavior: Clip.antiAlias,
      alignment: Alignment.center,
      child: url == null ? fallback() : Image.network(url, fit: BoxFit.cover, errorBuilder: (_, _, _) => fallback()),
    );
  }

  @override
  Widget build(BuildContext context) {
    final position = widget.controller?.value.position ?? Duration.zero;
    final duration = widget.controller?.value.duration ?? Duration.zero;
    final progress =
        duration.inMilliseconds > 0 ? (position.inMilliseconds / duration.inMilliseconds).clamp(0.0, 1.0) : 0.0;
    final rows = _rows();
    bool has(_OsdItem i) => rows.any((r) => r.contains(i));

    return Stack(
      key: const Key('channel-osd'),
      children: [
        Positioned(
          top: 24,
          right: 32,
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              Text(
                _hhmm(DateTime.now()),
                key: const Key('osd-clock'),
                style: const TextStyle(color: Colors.white, fontSize: 28, fontWeight: FontWeight.w500),
              ),
              Text(
                'Ends ${_hhmm(widget.endAt)}',
                key: const Key('osd-ends-at'),
                style: const TextStyle(color: Colors.white70, fontSize: 14),
              ),
            ],
          ),
        ),
        Positioned(
          left: 0,
          right: 0,
          bottom: 0,
          child: Container(
            padding: const EdgeInsets.fromLTRB(32, 48, 32, 20),
            decoration: const BoxDecoration(
              gradient: LinearGradient(
                begin: Alignment.topCenter,
                end: Alignment.bottomCenter,
                colors: [Color(0x00000000), Color(0xCC000000)],
              ),
            ),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    _logo(),
                    const SizedBox(width: 16),
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            widget.itemLabel,
                            key: const Key('osd-item-title'),
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: const TextStyle(color: Colors.white, fontSize: 26, fontWeight: FontWeight.bold),
                          ),
                          const SizedBox(height: 4),
                          Row(
                            children: [
                              Text(
                                '${widget.channelNumber}',
                                key: const Key('osd-channel-number'),
                                style: const TextStyle(color: Colors.white, fontSize: 15, fontWeight: FontWeight.bold),
                              ),
                              const Text(' · ', style: TextStyle(color: Colors.white70, fontSize: 15)),
                              Flexible(
                                child: Text(
                                  widget.channelName.toUpperCase(),
                                  key: const Key('osd-channel-name'),
                                  maxLines: 1,
                                  overflow: TextOverflow.ellipsis,
                                  style: const TextStyle(color: Colors.white70, fontSize: 15, letterSpacing: 0.5),
                                ),
                              ),
                              Text(
                                ' · ${_hhmm(widget.startAt)}–${_hhmm(widget.endAt)}',
                                key: const Key('osd-program-times'),
                                style: const TextStyle(color: Colors.white70, fontSize: 15),
                              ),
                              for (final c in _chips()) _chip(c),
                            ],
                          ),
                        ],
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: 8),
                // Transport controls: laid out now, wired by the follow-up
                // (seek / skip / back to live) — dimmed and never focused.
                Row(
                  children: [
                    _button(const Key('osd-btn-play-pause'), AppIcons.pause, enabled: false),
                    _button(const Key('osd-btn-previous'), AppIcons.skipPrevious, enabled: false),
                    _button(const Key('osd-btn-rewind'), AppIcons.rewind, enabled: false),
                    _button(const Key('osd-btn-fast-forward'), AppIcons.fastForward, enabled: false),
                    _button(const Key('osd-btn-next'), AppIcons.skipNext, enabled: false),
                  ],
                ),
                const SizedBox(height: 4),
                Row(
                  children: [
                    Expanded(
                      child: ClipRRect(
                        borderRadius: BorderRadius.circular(4),
                        child: LinearProgressIndicator(
                          key: const Key('osd-progress'),
                          value: progress,
                          minHeight: 6,
                          backgroundColor: Colors.white24,
                          valueColor: const AlwaysStoppedAnimation<Color>(Colors.lightBlueAccent),
                        ),
                      ),
                    ),
                    const SizedBox(width: 12),
                    Text(
                      '${_fmtDuration(position)} / ${_fmtDuration(duration)}',
                      style: const TextStyle(color: Colors.white70, fontSize: 13),
                    ),
                  ],
                ),
                const SizedBox(height: 8),
                Row(
                  children: [
                    if (has(_OsdItem.captions))
                      _button(const Key('osd-btn-captions'), AppIcons.captions, focused: _focused == _OsdItem.captions),
                    if (has(_OsdItem.audio))
                      _button(const Key('osd-btn-audio'), AppIcons.audioTrack, focused: _focused == _OsdItem.audio),
                    if (widget.nextItemLabel != null) ...[
                      const SizedBox(width: 16),
                      Flexible(
                        child: Text(
                          'Up next: ${widget.nextItemLabel}',
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: const TextStyle(color: Colors.white60, fontSize: 13, fontStyle: FontStyle.italic),
                        ),
                      ),
                    ],
                  ],
                ),
                _button(
                  const Key('osd-btn-channels'),
                  AppIcons.channels,
                  label: 'Channels',
                  focused: _focused == _OsdItem.channels,
                ),
              ],
            ),
          ),
        ),
      ],
    );
  }
}
