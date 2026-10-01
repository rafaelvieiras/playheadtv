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
  static const IconData channels = Symbols.live_tv;
  static const IconData history = Symbols.history;
  static const IconData clock = Symbols.schedule;
  static const IconData favorite = Symbols.star;
  static const IconData settings = Symbols.settings;
  static const IconData controls = Symbols.sports_esports;
}
