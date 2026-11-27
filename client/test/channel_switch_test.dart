import 'package:client/channel_switch.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('the delay defaults to 3 seconds and can be off', () {
    expect(sanitizeChannelSwitchDelaySeconds(null), defaultChannelSwitchDelaySeconds);
    expect(defaultChannelSwitchDelaySeconds, 3);
    expect(sanitizeChannelSwitchDelaySeconds(0), 0);
    expect(sanitizeChannelSwitchDelaySeconds(2), 2);
    expect(sanitizeChannelSwitchDelaySeconds(99), channelSwitchDelayChoicesSeconds.last);
    expect(sanitizeChannelSwitchDelaySeconds(-4), defaultChannelSwitchDelaySeconds);
    expect(channelSwitchDelayLabel(0), 'Off');
  });

  test('Select cycles through the delay choices and wraps', () {
    expect(nextChannelSwitchDelayChoice(0), 1);
    expect(nextChannelSwitchDelayChoice(1), 2);
    expect(nextChannelSwitchDelayChoice(3), 5);
    expect(nextChannelSwitchDelayChoice(channelSwitchDelayChoicesSeconds.last), channelSwitchDelayChoicesSeconds.first);
  });

  // The load starts when the delay ends: the whole window belongs to the channel
  // the viewer is still watching (see channelSwitchLoadAt for why the head start
  // was removed).
  test('the load starts when the delay ends', () {
    expect(channelSwitchLoadAt(Duration.zero), Duration.zero);
    expect(channelSwitchLoadAt(const Duration(seconds: 1)), const Duration(seconds: 1));
    expect(channelSwitchLoadAt(const Duration(seconds: 3)), const Duration(seconds: 3));
    expect(channelSwitchLoadAt(const Duration(seconds: 10)), const Duration(seconds: 10));
  });

  test('the banner lives 6 seconds by default, after the picture is up', () {
    expect(sanitizeChannelBannerSeconds(null), defaultChannelBannerSeconds);
    expect(defaultChannelBannerSeconds, 6);
    expect(nextChannelBannerChoice(3), 5);
    expect(nextChannelBannerChoice(15), 3);
    expect(channelBannerLabel(6), '6 seconds');
    // The hard cap always outlives the delay plus the banner: a channel that
    // never comes up still loses the banner, but not before the delay is over.
    expect(bannerHardLifetime(const Duration(seconds: 10), const Duration(seconds: 15)),
        greaterThan(const Duration(seconds: 25)));
  });
}
