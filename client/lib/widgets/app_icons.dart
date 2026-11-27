// Single place the client gets icons from. Swapping the icon library means
// editing only this file; call sites use `AppIcons.<name>`, never
// `Icons.*` or the package's `Symbols.*` directly.
import 'package:flutter/widgets.dart';
import 'package:material_symbols_icons/symbols.dart';

abstract final class AppIcons {
  static const IconData play = Symbols.play_arrow;
  static const IconData pause = Symbols.pause;
  static const IconData skipPrevious = Symbols.skip_previous;
  static const IconData skipNext = Symbols.skip_next;
  static const IconData rewind = Symbols.fast_rewind;
  static const IconData fastForward = Symbols.fast_forward;
  static const IconData captions = Symbols.closed_caption;
  static const IconData audioTrack = Symbols.music_note;
  static const IconData volume = Symbols.volume_up;
  static const IconData channels = Symbols.live_tv;
  static const IconData history = Symbols.history;
  static const IconData clock = Symbols.schedule;
  static const IconData favorite = Symbols.star;
  static const IconData settings = Symbols.settings;
  static const IconData controls = Symbols.sports_esports;
  static const IconData offlineCache = Symbols.cloud_sync;
  static const IconData fullScreen = Symbols.fullscreen;
  static const IconData interfaceSize = Symbols.format_size;
  static const IconData preloadLead = Symbols.hourglass_top;
  static const IconData dimmer = Symbols.brightness_medium;
  static const IconData debugLog = Symbols.terminal;
  static const IconData epgThumbnail = Symbols.picture_in_picture;
  static const IconData diagnostics = Symbols.bug_report;
  static const IconData logLevel = Symbols.tune;
  static const IconData videoEngine = Symbols.movie;
  static const IconData plugin = Symbols.extension;
  static const IconData pluginOff = Symbols.extension_off;
  static const IconData pluginMissing = Symbols.extension_off;
  static const IconData switchDelay = Symbols.timer;
  static const IconData bannerCorner = Symbols.label;
  static const IconData bannerTime = Symbols.schedule;
}
