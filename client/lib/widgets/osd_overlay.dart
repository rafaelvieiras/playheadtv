// Channel OSD: the info bar shown over the video — channel logo, program
// title, `number · name · start–end`, stream chips, the transport control
// row, a progress bar, the CC / audio-track buttons and a "Channels" section
// with the most recently watched channels for quick access. Clock (local time + when the current program ends) sits in the
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
// Select shows it and arms it; the first arrow after that turns on `focusMode`
// (a second Select instead opens the full EPG). In focus mode the parent
// relays the raw arrows to `move` and Select to `activateFocused`, and the
// focused control is outlined.
// Controls that do nothing yet (play/pause, previous/next, rewind/fast-
// forward) are rendered dimmed and skipped by focus.
import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import '../video/video_engine.dart';

import '../cache/offline_runtime.dart';
import '../dimmer.dart';
import '../osd_subject.dart';
import '../ui/ui_scale.dart';
import 'app_icons.dart';
import 'art_image.dart';
import 'channel_logo.dart';

/// A recently watched channel offered in the OSD's "Channels" section.
class OsdChannelShortcut {
  final String id;
  final int number;
  final String name;
  final String? artUrl;
  const OsdChannelShortcut({required this.id, required this.number, required this.name, this.artUrl});
}

// Focus items are plain strings: the two track buttons, then one entry per
// recent channel (`channel:<id>`).
const _captions = 'captions';
const _audio = 'audio';
// The dimmer toggle, and (only while dimmed) its level chip: Select on the
// chip opens the level dialog, the 6-button way to it from the OSD.
const _dimmer = 'dimmer';
const _dimmerLevel = 'dimmer-level';
const _channelPrefix = 'channel:';

class ChannelOsd extends StatefulWidget {
  final int channelNumber;
  final String channelName;
  final String? channelArtUrl;
  // The image id of the channel's uploaded logo, drawn in place of the channel
  // name (null: the name is printed, with the guide art in the leading badge).
  final String? channelLogoImageId;
  final String itemLabel;
  final String? nextItemLabel;
  final DateTime startAt;
  final DateTime endAt;
  // When true, the progress bar and the time readout run on the wall clock
  // across the displayed span [startAt, endAt] (a program plus its filler, as
  // the guide draws it) instead of following the clip on air.
  final bool progressByClock;
  // Re-reads what the OSD describes on every tick, so a subject that changes
  // with the clock (a tail past its attribution cap becomes the channel's)
  // updates without the parent rebuilding. When given, it wins over the
  // item/next/start/end props above, which stay as the fallback.
  final OsdSubject? Function()? resolveSubject;
  // Where the picture is in the schedule (see main.dart `_picturePlayhead`):
  // the progress bar and the elapsed time follow it, so a program that begins
  // reads 0:00 even when the wall clock is a little ahead of the picture. Null
  // result (or no callback): the wall clock.
  final DateTime? Function()? playhead;
  final VideoPlayback? controller;
  final ValueListenable<bool> focusMode;
  final VoidCallback onOpenSubtitles;
  final VoidCallback onOpenAudio;
  final VoidCallback onToggleDimmer;
  final VoidCallback onOpenDimmerLevel;
  final List<OsdChannelShortcut> recentChannels;
  final void Function(String channelId) onSelectChannel;

  /// The wall clock; tests pass a fixed one.
  final DateTime Function() now;

  const ChannelOsd({
    required Key key,
    required this.channelNumber,
    required this.channelName,
    required this.channelArtUrl,
    this.channelLogoImageId,
    required this.itemLabel,
    required this.nextItemLabel,
    required this.startAt,
    required this.endAt,
    this.progressByClock = false,
    this.resolveSubject,
    this.playhead,
    this.positionOffset,
    required this.controller,
    required this.focusMode,
    required this.onOpenSubtitles,
    required this.onOpenAudio,
    required this.onToggleDimmer,
    required this.onOpenDimmerLevel,
    required this.recentChannels,
    required this.onSelectChannel,
    this.now = DateTime.now,
  }) : super(key: key);

  /// Where the playback started inside the item, read on every build: a
  /// callback, not a value, because this widget is built once and kept across
  /// the player's rebuilds (AGENTS.md, "Client app structure") — a value passed
  /// at construction would stay at whatever it was on the first frame.
  final Duration Function()? positionOffset;

  @override
  State<ChannelOsd> createState() => ChannelOsdState();
}

/// The position the OSD shows. A playback whose source already began at an
/// offset reports its position from that stream's own zero, so the offset is
/// added back and the result never runs past the end.
Duration osdPosition({required Duration engine, required Duration offset, required Duration duration}) {
  final sum = engine + offset;
  if (sum.isNegative) return Duration.zero;
  if (duration > Duration.zero && sum > duration) return duration;
  return sum;
}

class ChannelOsdState extends State<ChannelOsd> {
  // Pixels at 1080p scaled to this screen (the app-wide UiScale). Reading it
  // subscribes only this widget, never the player page.
  double _u(double px) => context.ui(px);

  Timer? _tickTimer;
  String? _focused;

  @override
  void initState() {
    super.initState();
    _tickTimer = Timer.periodic(const Duration(milliseconds: 500), (_) {
      if (mounted) setState(() {});
    });
    widget.focusMode.addListener(_onFocusModeChanged);
    Dimmer.active.addListener(_onDimmerChanged);
    Dimmer.level.addListener(_onDimmerChanged);
  }

  // Repaints the dimmer button/chip. Only this widget rebuilds, never the page.
  void _onDimmerChanged() {
    if (mounted) setState(() {});
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
    Dimmer.active.removeListener(_onDimmerChanged);
    Dimmer.level.removeListener(_onDimmerChanged);
    _tickTimer?.cancel();
    super.dispose();
  }

  // Entering focus mode starts on the first available control; leaving it
  // clears the outline.
  void _onFocusModeChanged() {
    setState(() => _focused = widget.focusMode.value ? _rows().firstOrNull?.firstOrNull : null);
  }

  // Focusable rows, top to bottom: [CC, audio] then the recent channels. CC
  // needs a real subtitle track and audio needs a real choice (>1 track).
  List<List<String>> _rows() {
    final tracks = widget.controller?.tracks ?? MediaTracks.none;
    final hasSubtitles = tracks.subtitle.isNotEmpty;
    final hasAudioChoice = tracks.audio.length > 1;
    return [
      [if (hasSubtitles) _captions, if (hasAudioChoice) _audio, _dimmer, if (Dimmer.active.value) _dimmerLevel],
      [for (final c in widget.recentChannels) '$_channelPrefix${c.id}'],
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

  /// Whether the dimmer toggle holds the focus (its Select also has a hold
  /// gesture, handled by the parent).
  bool get dimmerFocused => widget.focusMode.value && _focused == _dimmer;

  /// Relayed by the parent on Select while in focus mode.
  void activateFocused() {
    final focused = _focused;
    if (focused == _dimmer) {
      widget.onToggleDimmer();
    } else if (focused == _dimmerLevel) {
      if (Dimmer.active.value) widget.onOpenDimmerLevel();
    } else if (focused == _captions) {
      widget.onOpenSubtitles();
    } else if (focused == _audio) {
      widget.onOpenAudio();
    } else if (focused != null && focused.startsWith(_channelPrefix)) {
      widget.onSelectChannel(focused.substring(_channelPrefix.length));
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
    final tracks = widget.controller?.tracks ?? MediaTracks.none;
    final chips = <String>[];
    final video = tracks.video.firstOrNull;
    if (video != null) {
      final h = video.height;
      if (h >= 720) chips.add('HD');
      if (h >= 2160) {
        chips.add('4K');
      } else if (h > 0) {
        chips.add('${h}p');
      }
    }
    final audio = tracks.audio.firstOrNull;
    if (audio != null) {
      chips.add(switch (audio.channels) {
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
        margin: EdgeInsets.only(left: _u(8)),
        padding: EdgeInsets.symmetric(horizontal: _u(6), vertical: _u(1)),
        decoration: BoxDecoration(
          color: Colors.white24,
          borderRadius: BorderRadius.circular(_u(3)),
        ),
        child: Text(text, style: TextStyle(color: Colors.white, fontSize: _u(11), fontWeight: FontWeight.bold)),
      );

  Widget _button(Key key, IconData icon, {String? label, bool enabled = true, bool focused = false}) {
    return Opacity(
      opacity: enabled ? 1 : 0.3,
      child: Container(
        key: key,
        padding: EdgeInsets.symmetric(horizontal: _u(label == null ? 10 : 12), vertical: _u(8)),
        decoration: BoxDecoration(
          color: focused ? Colors.white24 : Colors.transparent,
          border: Border.all(color: focused ? Colors.white : Colors.transparent, width: _u(2)),
          borderRadius: BorderRadius.circular(_u(8)),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, color: Colors.white, size: _u(28)),
            if (label != null) ...[
              SizedBox(width: _u(8)),
              Text(label, style: TextStyle(color: Colors.white, fontSize: _u(16))),
            ],
          ],
        ),
      ),
    );
  }

  Widget _channelCard(OsdChannelShortcut c, {required bool focused}) {
    final url = c.artUrl;
    Widget fallback() => Icon(AppIcons.channels, color: Colors.white, size: _u(20));
    return Container(
      key: Key('osd-recent-${c.id}'),
      width: _u(170),
      margin: EdgeInsets.only(right: _u(12)),
      padding: EdgeInsets.all(_u(8)),
      decoration: BoxDecoration(
        color: focused ? Colors.white24 : Colors.white10,
        border: Border.all(color: focused ? Colors.white : Colors.transparent, width: _u(2)),
        borderRadius: BorderRadius.circular(_u(8)),
      ),
      child: Row(
        children: [
          Container(
            width: _u(36),
            height: _u(36),
            decoration: const BoxDecoration(color: Colors.white12, shape: BoxShape.circle),
            clipBehavior: Clip.antiAlias,
            alignment: Alignment.center,
            child: url == null ? fallback() : ArtImage(url, fallback: (_) => fallback()),
          ),
          SizedBox(width: _u(8)),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text('${c.number}', style: TextStyle(color: Colors.white, fontSize: _u(14), fontWeight: FontWeight.bold)),
                Text(
                  c.name,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(color: Colors.white70, fontSize: _u(12)),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }

  /// The uploaded logo's URL, or null when the channel has none (the name is
  /// printed then, with the guide art in the leading badge).
  String? get _logoUrl {
    final imageId = widget.channelLogoImageId;
    if (imageId == null || imageId.isEmpty) return null;
    final runtime = OfflineRuntime.current;
    if (runtime == null) return null;
    return runtime.api.channelLogoUrl(imageId);
  }

  Widget _logo() {
    Widget fallback() => Icon(AppIcons.channels, color: Colors.white, size: _u(32));
    final url = widget.channelArtUrl;
    return Container(
      key: const Key('osd-channel-logo'),
      width: _u(64),
      height: _u(64),
      decoration: const BoxDecoration(color: Colors.white12, shape: BoxShape.circle),
      clipBehavior: Clip.antiAlias,
      alignment: Alignment.center,
      child: url == null ? fallback() : ArtImage(url, fallback: (_) => fallback()),
    );
  }

  /// The channel identity in the header: the uploaded logo when the channel
  /// has one, otherwise its name (upper-cased, the same key either way so a
  /// screen reader and the tests find one widget for it).
  Widget _channelName(String? logoUrl) {
    final name = Text(
      widget.channelName.toUpperCase(),
      key: const Key('osd-channel-name'),
      maxLines: 1,
      overflow: TextOverflow.ellipsis,
      style: TextStyle(color: Colors.white70, fontSize: _u(15), letterSpacing: 0.5),
    );
    if (logoUrl == null) return name;
    return LogoBox(
      maxHeight: _u(26),
      maxWidth: _u(220),
      child: ChannelLogo(
        logoUrl,
        height: _u(26),
        imageKey: const Key('osd-channel-logo-inline'),
        fallback: (_) => name,
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final logoUrl = _logoUrl;
    final subject = widget.resolveSubject?.call();
    final channelOnly = subject?.channelOnly ?? false;
    final itemLabel = channelOnly ? widget.channelName : (subject?.program.displayTitle ?? widget.itemLabel);
    final nextItemLabel = subject != null ? subject.next?.displayTitle : widget.nextItemLabel;
    final startAt = subject?.start ?? widget.startAt;
    final endAt = subject?.end ?? widget.endAt;
    final progressByClock = subject != null || widget.progressByClock;
    final Duration position;
    final Duration duration;
    if (channelOnly) {
      position = Duration.zero;
      duration = Duration.zero;
    } else if (progressByClock) {
      duration = endAt.difference(startAt);
      final elapsed = (widget.playhead?.call() ?? widget.now()).toUtc().difference(startAt);
      position = elapsed.isNegative ? Duration.zero : (elapsed > duration ? duration : elapsed);
    } else {
      position = osdPosition(
        engine: widget.controller?.state.position ?? Duration.zero,
        offset: widget.positionOffset?.call() ?? Duration.zero,
        duration: widget.controller?.state.duration ?? Duration.zero,
      );
      duration = widget.controller?.state.duration ?? Duration.zero;
    }
    final progress =
        duration.inMilliseconds > 0 ? (position.inMilliseconds / duration.inMilliseconds).clamp(0.0, 1.0) : 0.0;
    final rows = _rows();
    bool has(String i) => rows.any((r) => r.contains(i));

    return Stack(
      key: const Key('channel-osd'),
      children: [
        Positioned(
          top: _u(24),
          right: _u(32),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              Text(
                _hhmm(widget.now()),
                key: const Key('osd-clock'),
                style: TextStyle(color: Colors.white, fontSize: _u(28), fontWeight: FontWeight.w500),
              ),
              if (!channelOnly)
                Text(
                  'Ends ${_hhmm(endAt)}',
                  key: const Key('osd-ends-at'),
                  style: TextStyle(color: Colors.white70, fontSize: _u(14)),
                ),
            ],
          ),
        ),
        Positioned(
          left: 0,
          right: 0,
          bottom: 0,
          child: Container(
            padding: EdgeInsets.fromLTRB(_u(32), _u(48), _u(32), _u(20)),
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
                    // With an uploaded logo the badge is redundant: the logo
                    // itself takes the name's place below (see _channelName).
                    if (logoUrl == null) ...[
                      _logo(),
                      SizedBox(width: _u(16)),
                    ],
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            itemLabel,
                            key: const Key('osd-item-title'),
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: TextStyle(color: Colors.white, fontSize: _u(26), fontWeight: FontWeight.bold),
                          ),
                          SizedBox(height: _u(4)),
                          Row(
                            children: [
                              Text(
                                '${widget.channelNumber}',
                                key: const Key('osd-channel-number'),
                                style: TextStyle(color: Colors.white, fontSize: _u(15), fontWeight: FontWeight.bold),
                              ),
                              Text(' · ', style: TextStyle(color: Colors.white70, fontSize: _u(15))),
                              Flexible(child: _channelName(logoUrl)),
                              if (!channelOnly)
                              Text(
                                ' · ${_hhmm(startAt)}–${_hhmm(endAt)}',
                                key: const Key('osd-program-times'),
                                style: TextStyle(color: Colors.white70, fontSize: _u(15)),
                              ),
                              for (final c in _chips()) _chip(c),
                            ],
                          ),
                        ],
                      ),
                    ),
                  ],
                ),
                SizedBox(height: _u(8)),
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
                SizedBox(height: _u(4)),
                Row(
                  children: [
                    Expanded(
                      child: ClipRRect(
                        borderRadius: BorderRadius.circular(_u(4)),
                        child: LinearProgressIndicator(
                          key: const Key('osd-progress'),
                          value: progress,
                          minHeight: _u(6),
                          backgroundColor: Colors.white24,
                          valueColor: const AlwaysStoppedAnimation<Color>(Colors.lightBlueAccent),
                        ),
                      ),
                    ),
                    if (!channelOnly) ...[
                      SizedBox(width: _u(12)),
                      Text(
                        '${_fmtDuration(position)} / ${_fmtDuration(duration)}',
                        key: const Key('osd-elapsed'),
                        style: TextStyle(color: Colors.white70, fontSize: _u(13)),
                      ),
                    ],
                  ],
                ),
                SizedBox(height: _u(8)),
                Row(
                  children: [
                    if (has(_captions))
                      _button(const Key('osd-btn-captions'), AppIcons.captions, focused: _focused == _captions),
                    if (has(_audio))
                      _button(const Key('osd-btn-audio'), AppIcons.audioTrack, focused: _focused == _audio),
                    // Tap toggles, a long press opens the level dialog (mouse
                    // and touch accelerators; keys go through focus mode).
                    GestureDetector(
                      onTap: widget.onToggleDimmer,
                      onLongPress: widget.onOpenDimmerLevel,
                      child: _button(const Key('osd-btn-dimmer'), AppIcons.dimmer, focused: _focused == _dimmer),
                    ),
                    if (Dimmer.active.value)
                      _button(
                        const Key('osd-btn-dimmer-level'),
                        AppIcons.dimmer,
                        label: dimLevelLabel(Dimmer.level.value),
                        focused: _focused == _dimmerLevel,
                      ),
                    if (nextItemLabel != null) ...[
                      SizedBox(width: _u(16)),
                      Flexible(
                        child: Text(
                          'Up next: $nextItemLabel',
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: TextStyle(color: Colors.white60, fontSize: _u(13), fontStyle: FontStyle.italic),
                        ),
                      ),
                    ],
                  ],
                ),
                if (widget.recentChannels.isNotEmpty) ...[
                  SizedBox(height: _u(12)),
                  Text(
                    'Channels',
                    key: const Key('osd-channels-title'),
                    style: TextStyle(color: Colors.white, fontSize: _u(16), fontWeight: FontWeight.bold),
                  ),
                  SizedBox(height: _u(8)),
                  SingleChildScrollView(
                    scrollDirection: Axis.horizontal,
                    child: Row(
                      children: [
                        for (final c in widget.recentChannels)
                          _channelCard(c, focused: _focused == '$_channelPrefix${c.id}'),
                      ],
                    ),
                  ),
                ],
              ],
            ),
          ),
        ),
      ],
    );
  }
}
