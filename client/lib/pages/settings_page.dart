// Settings screen: "Log level" (until restart), "Diagnostics" (see diagnostics_page.dart), "Remap controls" (see controls_mapping_page.dart), "Offline
// cache" (see offline_cache_page.dart) and, on desktop, "Start in full screen".
//
// Operable with the six baseline buttons: Up/Down move the focus between the
// rows, Select opens one (or flips the full screen row) and Back (Esc by
// default) returns to the mosaic.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../input/input_action.dart';
import '../main.dart' show AppConfig;
import '../services/app_window.dart';
import '../dimmer.dart';
import '../preload_lead.dart';
import '../services/settings_service.dart';
import '../ui/ui_scale.dart';
import '../log/log.dart';
import 'controls_mapping_page.dart';
import 'diagnostics_page.dart';
import 'offline_cache_page.dart';
import 'setup_page.dart';
import '../widgets/app_icons.dart';
import '../widgets/dimmer_level_dialog.dart';

class SettingsPage extends StatefulWidget {
  const SettingsPage({super.key});

  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage> {
  KeyBindingsService? _keyBindings;
  SettingsService? _settings;

  @override
  void initState() {
    super.initState();
    unawaited(SettingsService.create().then((settings) {
      if (!mounted) return;
      setState(() {
        _settings = settings;
        _keyBindings = KeyBindingsService(settings);
      });
    }));
  }

  // Saves the choice and applies it to the window right away.
  void _toggleFullScreen() {
    final settings = _settings;
    if (settings == null) return;
    final next = !settings.startFullScreen(AppConfig.startFullScreenDefault);
    unawaited(settings.setStartFullScreen(next).then((_) {
      if (mounted) setState(() {});
    }));
    unawaited(FullScreenController.shared.apply(next));
  }

  // Cycles Automatic, 75%, 100%, ... and applies the choice at once.
  void _cycleUiScale() {
    final settings = _settings;
    if (settings == null) return;
    final next = nextUiScaleChoice(settings.uiScaleOverride);
    unawaited(settings.setUiScaleOverride(next).then((_) {
      if (mounted) setState(() {});
    }));
    UiScaleOverride.notifier.value = next;
  }

  // Select steps through the preload lead choices (seconds) and saves.
  void _cyclePreloadLead() {
    final settings = _settings;
    if (settings == null) return;
    final current = settings.preloadLeadSeconds ?? AppConfig.preloadLeadDefaultSeconds;
    unawaited(settings.setPreloadLeadSeconds(nextPreloadLeadChoice(current)).then((_) {
      if (mounted) setState(() {});
    }));
  }

  // Select steps through the log levels. Applies at once and only until the app
  // restarts (never saved): a forgotten verbose client must not keep logging.
  void _cycleLogLevel() {
    Log.setRuntimeLevel(nextLogLevelChoice(Log.level));
    setState(() {});
  }

  // Opens the dim level dialog (six buttons) and saves the confirmed level.
  Future<void> _editDimLevel() async {
    final settings = _settings;
    final keyBindings = _keyBindings;
    if (settings == null || keyBindings == null) return;
    final chosen = await showDimmerLevelDialog(
      context,
      initial: AppConfig.configuredDimLevel(settings),
      resolve: keyBindings.resolve,
    );
    if (chosen == null) return;
    await settings.setDimLevelPercent(chosen);
    Dimmer.level.value = chosen;
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    return UiScaled(child: _buildPage(context));
  }

  Widget _buildPage(BuildContext context) {
    return Focus(
      // Only catches Back bubbling up from a row: it must never be a traversal
      // target itself (it would take focus past the last row).
      canRequestFocus: false,
      skipTraversal: true,
      onKeyEvent: (node, event) {
        if (event is KeyDownEvent && _keyBindings?.resolve(event.logicalKey) == InputAction.back) {
          Navigator.of(context).maybePop();
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: Scaffold(
        key: const Key('settings-page'),
        appBar: AppBar(title: const Text('Settings'), toolbarHeight: context.ui(kToolbarHeight)),
        body: ListView(
          children: [
            ListTile(
              key: const Key('settings-connection-tile'),
              leading: const Icon(Icons.lan),
              title: const Text('Connection'),
              subtitle: const Text('Media server and Playhead TV addresses'),
              onTap: () {
                Navigator.of(context).push(MaterialPageRoute(builder: (_) => const SetupPage()));
              },
            ),
            ListTile(
              key: const Key('settings-controls-tile'),
              autofocus: true,
              leading: const Icon(AppIcons.controls),
              title: const Text('Remap controls'),
              onTap: () {
                Navigator.of(context).push(MaterialPageRoute(builder: (_) => const ControlsMappingPage()));
              },
            ),
            ListTile(
              key: const Key('settings-offline-tile'),
              leading: const Icon(AppIcons.offlineCache),
              title: const Text('Offline cache'),
              onTap: () {
                Navigator.of(context).push(MaterialPageRoute(builder: (_) => const OfflineCachePage()));
              },
            ),
            // The window is not the app's to resize on Android (always full
            // screen): no row there.
            if (_settings != null && FullScreenController.shared.supported)
              ListTile(
                key: const Key('settings-fullscreen-tile'),
                leading: const Icon(AppIcons.fullScreen),
                title: const Text('Start in full screen'),
                subtitle: Text(
                  _settings!.startFullScreen(AppConfig.startFullScreenDefault) ? 'On' : 'Off',
                  key: const Key('settings-fullscreen-value'),
                ),
                onTap: _toggleFullScreen,
              ),
            if (_settings != null)
              ListTile(
                key: const Key('settings-ui-scale-tile'),
                leading: const Icon(AppIcons.interfaceSize),
                title: const Text('Interface size'),
                subtitle: Text(
                  uiScaleLabel(_settings!.uiScaleOverride),
                  key: const Key('settings-ui-scale-value'),
                ),
                onTap: _cycleUiScale,
              ),
            if (_settings != null)
              ListTile(
                key: const Key('settings-preload-lead-tile'),
                leading: const Icon(AppIcons.preloadLead),
                title: const Text('Preload lead'),
                subtitle: Text(
                  preloadLeadLabel(_settings!.preloadLeadSeconds ?? AppConfig.preloadLeadDefaultSeconds),
                  key: const Key('settings-preload-lead-value'),
                ),
                onTap: _cyclePreloadLead,
              ),
            if (_settings != null)
              ListTile(
                key: const Key('settings-dim-level-tile'),
                leading: const Icon(AppIcons.dimmer),
                title: const Text('Dim level'),
                subtitle: Text(
                  dimLevelLabel(AppConfig.configuredDimLevel(_settings!)),
                  key: const Key('settings-dim-level-value'),
                ),
                onTap: _editDimLevel,
              ),
            ListTile(
              key: const Key('settings-log-level-tile'),
              leading: const Icon(AppIcons.logLevel),
              title: const Text('Log level'),
              subtitle: Text(logLevelLabel(Log.level, Log.configuredLevel), key: const Key('settings-log-level-value')),
              onTap: _cycleLogLevel,
            ),
            ListTile(
              key: const Key('settings-diagnostics-tile'),
              leading: const Icon(AppIcons.diagnostics),
              title: const Text('Diagnostics'),
              subtitle: const Text('Log file location and a shareable log bundle'),
              onTap: () {
                Navigator.of(context).push(MaterialPageRoute(builder: (_) => const DiagnosticsPage()));
              },
            ),
          ],
        ),
      ),
    );
  }
}
