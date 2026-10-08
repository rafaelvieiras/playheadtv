// Settings -> "Offline cache": the client's own choices about the local copy of
// the schedule (see docs/ARCHITECTURE.md "Client offline cache"), all of them
// reachable and changeable with the six baseline buttons (Up/Down move the
// focus between rows, Select changes the value or runs the action, Back
// leaves):
//
//  - what a channel does when the cached schedule has run out (offline for
//    longer than the window): loop the last known programming (default) or
//    hold the local slate. Select flips it.
//  - the cache window limit (the effective window is the smaller of this and
//    the backend's). Select steps to the next choice, wrapping around.
//  - sync now, with the time since the last refresh and how long playback
//    could go on with no backend.
//  - clear the cache (an explicit action, behind a confirmation).
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../cache/offline_runtime.dart';
import '../cache/schedule_sync.dart';
import '../input/input_action.dart';
import '../services/settings_service.dart';
import '../ui/ui_scale.dart';

String exhaustedBehaviorLabel(ExhaustedBehavior b) => switch (b) {
  ExhaustedBehavior.loopLast => 'Loop the last known programming',
  ExhaustedBehavior.slate => 'Hold the slate',
};

String hoursLabel(Duration d) => '${d.inHours} hours';

String _spanLabel(Duration d) {
  if (d.isNegative) return '0m';
  final h = d.inHours;
  final m = d.inMinutes % 60;
  return h > 0 ? '${h}h ${m}m' : '${m}m';
}

class OfflineCachePage extends StatefulWidget {
  const OfflineCachePage({super.key});

  @override
  State<OfflineCachePage> createState() => _OfflineCachePageState();
}

class _OfflineCachePageState extends State<OfflineCachePage> {
  OfflineRuntime? _runtime;
  KeyBindingsService? _keyBindings;
  Timer? _tick;

  @override
  void initState() {
    super.initState();
    unawaited(_bootstrap());
    // The "last sync" line ages on its own.
    _tick = Timer.periodic(const Duration(seconds: 30), (_) {
      if (mounted) setState(() {});
    });
  }

  Future<void> _bootstrap() async {
    final runtime = await OfflineRuntime.ensure();
    if (!mounted) return;
    setState(() {
      _runtime = runtime;
      _keyBindings = KeyBindingsService(runtime.settings);
    });
  }

  @override
  void dispose() {
    _tick?.cancel();
    super.dispose();
  }

  void _cycleExhausted() {
    final settings = _runtime!.settings;
    final next = settings.exhaustedBehavior == ExhaustedBehavior.loopLast
        ? ExhaustedBehavior.slate
        : ExhaustedBehavior.loopLast;
    unawaited(
      settings.setExhaustedBehavior(next).then((_) {
        if (mounted) setState(() {});
      }),
    );
  }

  void _cycleWindow() {
    final runtime = _runtime!;
    const choices = SettingsService.cacheWindowLimitChoicesHours;
    final current = runtime.settings.cacheWindowLimit.inHours;
    final at = choices.indexOf(current);
    final next = Duration(hours: choices[(at + 1) % choices.length]);
    unawaited(
      runtime.settings.setCacheWindowLimit(next).then((_) async {
        await runtime.applyWindowLimit(next);
        if (mounted) setState(() {});
      }),
    );
  }

  Future<void> _confirmClear() async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => UiScaled(
        child: AlertDialog(
          key: const Key('offline-clear-dialog'),
          title: const Text('Clear the offline cache?'),
          content: const Text(
            'The saved schedule and images are deleted and downloaded again when the backend is reachable.',
          ),
          actions: [
            TextButton(
              key: const Key('offline-clear-cancel'),
              autofocus: true,
              onPressed: () => Navigator.of(context).pop(false),
              child: const Text('Cancel'),
            ),
            TextButton(
              key: const Key('offline-clear-confirm'),
              onPressed: () => Navigator.of(context).pop(true),
              child: const Text('Clear'),
            ),
          ],
        ),
      ),
    );
    if (confirmed == true) {
      await _runtime!.clearCache();
      if (mounted) setState(() {});
    }
  }

  String _syncSubtitle(OfflineRuntime runtime, SyncStatus status) {
    final cache = runtime.cache;
    final last = cache.lastSyncAt;
    final lines = <String>[];
    if (status.syncing) {
      lines.add('Syncing…');
    } else if (last == null) {
      lines.add('Never synced');
    } else {
      lines.add('Last sync ${_spanLabel(cache.now().difference(last))} ago');
    }
    if (last != null) {
      final tolerance = cache.window - cache.now().difference(last);
      lines.add('Can play with no backend for about ${_spanLabel(tolerance)}');
    }
    if (!status.syncing && status.lastOutcome != null && !status.lastSucceeded) {
      lines.add(switch (status.lastOutcome!) {
        SyncOutcome.authRejected => 'The backend rejected the access token; playing from the cache',
        SyncOutcome.offline => 'The backend cannot be reached; playing from the cache',
        _ => 'The last sync failed; playing from the cache',
      });
    }
    return lines.join('\n');
  }

  @override
  Widget build(BuildContext context) => UiScaled(child: _buildPage(context));

  Widget _buildPage(BuildContext context) {
    final runtime = _runtime;
    if (runtime == null) {
      return const Scaffold(
        key: Key('offline-cache-page'),
        body: Center(child: CircularProgressIndicator()),
      );
    }
    final settings = runtime.settings;
    final window = runtime.cache.window;
    final backend = runtime.cache.backendWindow;
    return Focus(
      // Back (Esc by default, remappable) leaves; the arrows and Select are
      // the focus traversal and activation Flutter gives every tile.
      onKeyEvent: (node, event) {
        if (event is KeyDownEvent && _keyBindings!.resolve(event.logicalKey) == InputAction.back) {
          Navigator.of(context).maybePop();
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: Scaffold(
        key: const Key('offline-cache-page'),
        appBar: AppBar(title: const Text('Offline cache'), toolbarHeight: context.ui(kToolbarHeight)),
        body: ListView(
          children: [
            ListTile(
              key: const Key('offline-exhausted-tile'),
              autofocus: true,
              title: const Text('When the schedule runs out'),
              subtitle: Text(
                exhaustedBehaviorLabel(settings.exhaustedBehavior),
                key: const Key('offline-exhausted-value'),
              ),
              onTap: _cycleExhausted,
            ),
            ListTile(
              key: const Key('offline-window-tile'),
              title: const Text('Cache window limit'),
              subtitle: Text(
                '${hoursLabel(settings.cacheWindowLimit)}'
                '${backend == null ? '' : ' (backend keeps ${hoursLabel(backend)}; in use: ${hoursLabel(window)})'}',
                key: const Key('offline-window-value'),
              ),
              onTap: _cycleWindow,
            ),
            ValueListenableBuilder<SyncStatus>(
              valueListenable: runtime.syncer.status,
              builder: (context, status, _) => ListTile(
                key: const Key('offline-sync-tile'),
                title: const Text('Sync now'),
                subtitle: Text(_syncSubtitle(runtime, status), key: const Key('offline-sync-value')),
                onTap: () => unawaited(runtime.syncer.syncNow()),
              ),
            ),
            ListTile(
              key: const Key('offline-clear-tile'),
              title: const Text('Clear cache'),
              subtitle: const Text('Delete the saved schedule and images'),
              onTap: () => unawaited(_confirmClear()),
            ),
          ],
        ),
      ),
    );
  }
}
