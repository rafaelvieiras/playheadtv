// A `pair` on Android: two addresses, one timeline.
//
// The `video_player` plugin cannot do this (it opens one media source per
// player), so the app builds the merged player itself on Media3 and takes over
// the whole surface this file uses: it opens, it reports both kinds of track, it
// seeks and it keeps playing. That is what this suite checks, on a real Android
// runtime, before any of it reaches a television.
//
// What proves the merge, and not merely "something plays": the fixture's two
// files carry one track each — the video file has no audio (`-an`) and the audio
// file no picture (`-vn`) — so a playback that reports an audio track with a
// sample rate AND a video track with a frame size can only have opened both.
//
// Run it on the lab: client/tool/run_android_suites.sh exo_pair_test
import 'package:client/video/video_engine.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';

import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  final video = VideoFixture();

  setUpAll(() async {
    // The engine under test is the Android one, whichever one the app's settings
    // would pick on a desktop run of this file.
    selectVideoEngine(VideoEngineKind.exo);
    await video.start(pair: true);
  });

  tearDownAll(() async {
    await video.stop();
  });

  testWidgets('the engine declares it can take a pair, so a plugin is asked for one', (tester) async {
    expect(videoEngine.supportsPairedAudio, isTrue);
    expect(videoEngine.name, contains('ExoPlayer'));
    // The host's preference starts with the free, best-picture shape.
    expect(pluginPlaybackModes(videoEngine).first, 'pair');
  });

  testWidgets('a video url and an audio url play as one picture with sound', (tester) async {
    final playback = videoEngine.createNetwork(video.pairVideoUrl, audioUri: video.pairAudioUrl);
    expect(playback.source, video.pairVideoUrl.toString());
    await tester.pumpWidget(Directionality(
      textDirection: TextDirection.ltr,
      child: Center(child: VideoSurface(playback)),
    ));

    await playback.open();
    // The merged timeline is ready only once both sources are prepared; the
    // host answers `create` at that point, so an open that returned is a merge
    // that worked.
    expect(playback.state.isInitialized, isTrue);

    // The track list is a separate message from the host, so it can land a
    // sample after the open returns.
    final waited = DateTime.now();
    while (playback.tracks.audio.isEmpty && DateTime.now().difference(waited) < const Duration(seconds: 5)) {
      await tester.pump(const Duration(milliseconds: 100));
    }

    final tracks = playback.tracks;
    expect(tracks.video, isNotEmpty, reason: 'the video file\'s own track');
    expect(tracks.audio, isNotEmpty, reason: 'the audio file\'s own track, merged in');
    expect(tracks.video.first.width, 320);
    expect(tracks.video.first.height, 240);
    expect(tracks.audio.first.sampleRate, greaterThan(0));
    expect(tracks.audio.first.channels, greaterThan(0));

    await playback.seekTo(const Duration(seconds: 3));
    await playback.play();

    // The position has to move on its own, with the picture on screen behind it.
    final started = DateTime.now();
    var moved = Duration.zero;
    while (DateTime.now().difference(started) < const Duration(seconds: 6)) {
      await tester.pump(const Duration(milliseconds: 200));
      moved = playback.state.position;
      if (moved > const Duration(milliseconds: 4300)) break;
    }
    expect(playback.state.isPlaying, isTrue);
    expect(moved, greaterThan(const Duration(milliseconds: 4300)), reason: 'playback advanced from the seek');

    final size = playback.state.size;
    expect(size.width, 320);
    expect(size.height, 240);

    await playback.dispose();
    await tester.pumpWidget(const SizedBox.shrink());
  });
}
