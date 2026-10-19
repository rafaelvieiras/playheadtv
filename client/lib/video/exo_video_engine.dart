// ExoPlayer (AndroidX Media3) through the platform plugin `video_player_android`.
// Android only. Unlike libmdk it decodes with the platform MediaCodec, which is
// the path a TV box's hardware decoders are tuned for; the trade-off is what
// that plugin exposes (see the limits below) and that it decodes only what the
// device can: no software fallback for an unsupported audio or video codec.
//
// Known limits, all of the plugin and not of this app:
//  * Embedded subtitle tracks are not exposed, so [MediaTracks.subtitle] is
//    always empty and [VideoPlayback.selectSubtitles] does nothing (the OSD
//    disables its CC button, and a stream rule that demands a subtitle
//    language does not match).
//  * The read-ahead window of [PlaybackTuning] cannot be set (ExoPlayer's load
//    control is not reachable), so ExoPlayer's own defaults apply.
//  * Only one audio track plays at a time.
//
// Unlike [FvpVideoEngine] there is no native-lifecycle queue: ExoPlayer
// instances are independent, there is no process-global GL thread to share.
import 'dart:async';

import 'package:video_player/video_player.dart';

import '../log/log.dart';
import 'controller_playback.dart';
import 'language_code.dart';
import 'video_engine.dart';

class ExoVideoEngine implements VideoEngine {
  @override
  String get name => 'ExoPlayer (Media3)';

  @override
  String get decoderSummary => 'platform MediaCodec (hardware when available)';

  // The plugin registers itself as the `video_player` platform on Android, as
  // long as fvp's `registerWith` was not called (it replaces that platform).
  @override
  void register() {}

  // The plugin gives every ExoPlayer its own audio focus request by default,
  // and ExoPlayer pauses when another player takes the focus: the crossfade's
  // incoming player would pause the one fading out. The app plays one thing at
  // a time and fades the volumes itself, so no player asks for focus.
  static final _options = VideoPlayerOptions(mixWithOthers: true);

  @override
  VideoPlayback createNetwork(Uri uri) => _ExoPlayback(
        VideoPlayerController.networkUrl(uri, videoPlayerOptions: _options),
      );

  @override
  VideoPlayback createAsset(String assetPath) => _ExoPlayback(
        VideoPlayerController.asset(assetPath, videoPlayerOptions: _options),
      );
}

class _ExoPlayback extends ControllerPlayback {
  _ExoPlayback(super.controller);

  // The plugin reads tracks asynchronously, [tracks] is synchronous: they are
  // fetched once, in [open], and the lists below stay in the plugin's order,
  // which is the order the app's positions address.
  List<VideoAudioTrack> _audio = const [];
  MediaTracks _tracks = MediaTracks.none;

  @override
  MediaTracks get tracks => _tracks;

  @override
  Future<void> open({String label = 'initialize'}) async {
    await controller.initialize();
    await _readTracks();
  }

  Future<void> _readTracks() async {
    try {
      const bound = Duration(seconds: 2);
      final audio = await controller.getAudioTracks().timeout(bound);
      final video = await controller.getVideoTracks().timeout(bound);
      _audio = audio;
      _tracks = MediaTracks(
        video: [
          for (final t in video)
            VideoTrackInfo(
              codec: t.codec ?? '',
              width: t.width ?? 0,
              height: t.height ?? 0,
              frameRate: t.frameRate ?? 0,
              bitRate: t.bitrate ?? 0,
            ),
        ],
        audio: [
          for (final t in audio)
            AudioTrackInfo(
              language: canonicalLanguage(t.language),
              title: _nonEmpty(t.label),
              codec: t.codec ?? '',
              channels: t.channelCount ?? 0,
              sampleRate: t.sampleRate ?? 0,
              bitRate: t.bitrate ?? 0,
            ),
        ],
      );
    } catch (e, st) {
      // Disposed while asking, or a plugin that cannot say: the playback still
      // plays, it just has no track list (the player's own default audio).
      Log.warn('exo', 'could not read the track list', error: e, stack: st);
    }
  }

  @override
  void selectAudio(List<int> tracks) {
    if (tracks.isEmpty) return;
    // ExoPlayer plays one audio track at a time.
    final position = tracks.first;
    if (position < 0 || position >= _audio.length) return;
    unawaited(controller.selectAudioTrack(_audio[position].id).catchError((Object e, StackTrace st) {
      Log.warn('exo', 'could not select audio track $position', error: e, stack: st);
    }));
  }

  @override
  void selectSubtitles(List<int> tracks) {}

  @override
  void tune(PlaybackTuning tuning) {}

  @override
  Future<void> dispose() => controller.dispose();
}

String? _nonEmpty(String? value) => (value == null || value.trim().isEmpty) ? null : value;
