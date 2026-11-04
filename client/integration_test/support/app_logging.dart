// The app's own log, for a suite.
//
// Every suite boots `PlayheadSpikeApp` directly and never runs `main()`, so it
// inherits none of its setup — the log file included. Without this a failing
// suite leaves nothing: `flutter test` kills the app, the log lived in memory,
// and the report can only say "the screen did not show what the test expected".
// With it, the app's account of what happened is on disk in `files/`, which the
// host reads while the app still runs (`adb shell run-as <package> cat
// files/playheadtv.log`).
//
// Call first thing in `setUpAll`. Harmless on the desktop, where the log lands in
// the app's support directory instead of the install's state directory.
import 'package:client/log/log.dart';
import 'package:path_provider/path_provider.dart';

Future<void> startSuiteLogging({LogLevel level = LogLevel.debug}) async {
  try {
    final dir = await getApplicationSupportDirectory();
    Log.configure(level: level, directory: dir);
    Log.info('suite', 'logging to ${dir.path}');
  } catch (_) {
    // A platform without a support directory: the suite still runs, it just has
    // no log to leave behind.
  }
}
