// The "Show channel thumbnail in the guide" choice, shared by the player (which moves the video
// into the corner while the guide is open) and the guide (which leaves the matching hole).
// Static on purpose, like `Dimmer.level`: there is one player per process, and the choice
// cannot change while the guide is open (Settings is another screen).
//
// Off: the video stays where it is, full screen, and the guide draws an opaque layer over it,
// so the grid gets the screen height the thumbnail used. The video widgets are never moved,
// resized or rebuilt for it (the GL texture gotcha in main.dart).
import 'package:flutter/foundation.dart';

class EpgThumbnailSetting {
  static final ValueNotifier<bool> enabled = ValueNotifier<bool>(true);
}
