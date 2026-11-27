// The channel-change policy: how long the picture of the channel you just left
// stays on screen after a channel change, how much of that time the next channel
// gets to load, and how long the banner that announces the change stays up.
//
// Pure Dart, no Flutter: the numbers are unit-tested on their own
// (test/channel_switch_test.dart) and main.dart only wires them to the player.
// The behaviour they encode is in docs/ARCHITECTURE.md "Channel change".
/// The delay between pressing a channel and the picture changing, when the user
/// never chose one.
///
/// It is a debounce with a picture: a viewer surfing (five channels in three
/// seconds) sees the banner walk through channels while the stream they were
/// watching keeps playing, and only the channel they stop on costs the media
/// server anything.
const defaultChannelSwitchDelaySeconds = 3;

/// The values Select cycles through in Settings (seconds). Zero is the menu-less
/// "change now" this app always had.
const channelSwitchDelayChoicesSeconds = [0, 1, 2, 3, 5, 10];

/// A configured delay that is usable, else [defaultChannelSwitchDelaySeconds].
/// An in-range value is kept as is, an out-of-range one is clamped.
int sanitizeChannelSwitchDelaySeconds(Object? value) {
  if (value is! int || value < 0) return defaultChannelSwitchDelaySeconds;
  return value.clamp(0, channelSwitchDelayChoicesSeconds.last);
}

/// The next Settings choice when Select is pressed: the first choice above
/// [current], wrapping to the smallest.
int nextChannelSwitchDelayChoice(int current) {
  for (final c in channelSwitchDelayChoicesSeconds) {
    if (c > current) return c;
  }
  return channelSwitchDelayChoicesSeconds.first;
}

String channelSwitchDelayLabel(int seconds) => seconds == 0 ? 'Off' : '$seconds seconds';

/// How long Select must be held to open the guide directly (the same 500 ms the
/// guide itself uses for its long-press).
const selectHoldToGuide = Duration(milliseconds: 500);

/// The instant the real load starts, counted from the key press: when the delay
/// is over.
///
/// It used to start two seconds early, with the picture of the channel being left
/// held on screen while the next one opened — the effect that makes a change look
/// instant. That variant kept two controllers alive across the switch, and on
/// fvp/libmdk it wedged the frame pipeline when the outgoing controller had just
/// had a live `setAudioTracks` (measured: the suite's own `pump` never returned;
/// pausing the held playback, or not drawing it at all, did not help). The delay
/// therefore hides the *decision*, not the load: the picture changes when the
/// delay ends and the channel starts opening then.
Duration channelSwitchLoadAt(Duration delay) => delay <= Duration.zero ? Duration.zero : delay;

/// How long the banner stays up after the new picture is on screen, when the
/// user never chose a value.
const defaultChannelBannerSeconds = 6;

/// The values Select cycles through in Settings (seconds).
const channelBannerChoicesSeconds = [3, 5, 6, 10, 15];

int sanitizeChannelBannerSeconds(Object? value) {
  if (value is! int || value < 0) return defaultChannelBannerSeconds;
  return value.clamp(channelBannerChoicesSeconds.first, channelBannerChoicesSeconds.last);
}

int nextChannelBannerChoice(int current) {
  for (final c in channelBannerChoicesSeconds) {
    if (c > current) return c;
  }
  return channelBannerChoicesSeconds.first;
}

String channelBannerLabel(int seconds) => '$seconds seconds';

/// The guard that keeps a banner from outliving a channel that never comes up:
/// past this (counted from the key press) the banner goes away even if the
/// channel is still loading, and the spinner is left to say so.
Duration bannerHardLifetime(Duration delay, Duration banner) =>
    delay + banner + const Duration(seconds: 20);
