// The only file that knows `fvp` (libmdk) and the `video_player` plugin.
// Every engine-specific workaround lives here: the shared GL queue around
// native create/dispose and the `avsync.audio` / `buffer` properties.
import 'dart:io' show Platform;

import 'package:fvp/fvp.dart' as fvp;
import 'package:video_player/video_player.dart';

import '../util/gl_serial.dart';
import 'controller_playback.dart';
import 'video_engine.dart';

class FvpVideoEngine implements VideoEngine {
  @override
  String get name => 'fvp (libmdk)';

  @override
  String get decoderSummary => Platform.isAndroid
      ? 'platform default (AMediaCodec > FFmpeg)'
      : 'CUDA > VAAPI > VDPAU > FFmpeg';

  @override
  void register() {
    // video.decoders: fvp's default decoder order on Linux tries VAAPI before
    // CUDA/VDPAU. On some setups (e.g. NVIDIA without VAAPI configured) that
    // stalls decoding of HEVC videos (audio plays, "initialized" never fires):
    // the same Jellyfin URL plays smoothly with CUDA or FFmpeg but stalls with
    // the default order. Putting CUDA first avoids that and keeps VAAPI/VDPAU as
    // fallbacks for AMD/Intel machines (where CUDA simply fails fast and falls
    // through to the next decoder).
    // Desktop only: on Android that list has no hardware decoder, so a TV box
    // would decode in software (audio only, black picture). Leaving it unset
    // keeps fvp's own order there (AMediaCodec, then FFmpeg).
    fvp.registerWith(
      options: Platform.isAndroid
          ? const <String, Object>{}
          : const {
              'video.decoders': ['CUDA', 'VAAPI', 'VDPAU', 'FFmpeg'],
            },
    );
  }

  @override
  VideoPlayback createNetwork(Uri uri) =>
      _FvpPlayback(VideoPlayerController.networkUrl(uri));

  @override
  VideoPlayback createAsset(String assetPath) =>
      _FvpPlayback(VideoPlayerController.asset(assetPath));
}

class _FvpPlayback extends ControllerPlayback {
  _FvpPlayback(super.controller);

  // fvp/libmdk creates GL resources on its own raster thread during
  // initialize() and frees them on a separate dispose thread, and those
  // threads are process-global: two native lifecycle calls overlapping can
  // reuse a GL object id mid-teardown (a real segfault, see gl_serial.dart).
  // So every initialize()/dispose() goes through the one shared FIFO.
  @override
  Future<void> open({String label = 'initialize'}) =>
      serializeGlOp(() => controller.initialize(), label: label);

  @override
  Future<void> dispose() =>
      serializeGlOp(() => controller.dispose(), label: 'dispose');

  @override
  MediaTracks get tracks {
    final info = controller.getMediaInfo();
    if (info == null) return MediaTracks.none;
    return MediaTracks(
      video: [
        for (final s in info.video ?? const [])
          VideoTrackInfo(
            codec: s.codec.codec,
            profile: s.codec.profile,
            level: s.codec.level,
            width: s.codec.width,
            height: s.codec.height,
            frameRate: s.codec.frameRate,
            bitRate: s.codec.bitRate,
          ),
      ],
      audio: [
        for (final s in info.audio ?? const [])
          AudioTrackInfo(
            language: _tag(s.metadata, 'language'),
            title: _tag(s.metadata, 'title'),
            codec: s.codec.codec,
            channels: s.codec.channels,
            sampleRate: s.codec.sampleRate,
            bitRate: s.codec.bitRate,
          ),
      ],
      subtitle: [
        for (final s in info.subtitle ?? const [])
          SubtitleTrackInfo(
            language: _tag(s.metadata, 'language'),
            title: _tag(s.metadata, 'title'),
          ),
      ],
    );
  }

  // The mdk API takes a 0-based index within that media type, not
  // StreamInfo.index (the demuxer's absolute stream number), which is why
  // `tracks` keeps each list in mdk's order and the app only ever passes
  // positions in it.
  @override
  void selectAudio(List<int> tracks) => controller.setAudioTracks(tracks);

  // An explicit empty list is what disables libmdk's default-enabled first
  // MKV subtitle track.
  @override
  void selectSubtitles(List<int> tracks) =>
      controller.setSubtitleTracks(tracks);

  @override
  void tune(PlaybackTuning tuning) {
    // By default mdk syncs video frame presentation to the AUDIO clock — when
    // that sync stalls (same mechanism reported in wang-bin/mdk-sdk#179, root
    // cause confirmed by the maintainer: "bug comes from the audio
    // renderer"), the video freezes on the last frame while the audio, already
    // on its own queue, keeps playing normally. Confirmed by the debug overlay
    // showing a high, stable Flutter FPS during the freeze — it isn't
    // compositor jank, it's video presentation stuck in that sync.
    // 'avsync.audio'=0 decouples it and syncs to a stable clock instead of the
    // audio; the value must be "0"/"1" (not "false"/"true" — mdk calls stoi()
    // on the value and "false" kills the process with
    // std::invalid_argument).
    controller.setProperty('avsync.audio', '0');
    // Read-ahead window ("minMs+maxMs", mdk's setBufferRange): playback only
    // starts/resumes once `min` is buffered and the demuxer keeps reading up
    // to `max` — mdk's default is 1s..4s, too little for a source with bursty
    // latency (e.g. a network-mounted library), where each hiccup shows as a
    // stutter.
    final min = tuning.bufferMin;
    final max = tuning.bufferMax;
    if (min != null && max != null) {
      controller.setProperty(
        'buffer',
        '${min.inMilliseconds}+${max.inMilliseconds}',
      );
    }
  }
}

// Case-insensitive lookup into a track's ffmpeg-style metadata map: real-world
// containers are not consistent about casing, and the map is a plain
// Map<String, String> with no dedicated accessor.
String? _tag(Map<String, String> metadata, String key) {
  for (final entry in metadata.entries) {
    if (entry.key.toLowerCase() == key) return entry.value;
  }
  return null;
}
