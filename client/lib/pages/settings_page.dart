// Settings screen: "Remap controls" (see controls_mapping_page.dart), "Offline
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
import '../services/settings_service.dart';
import 'controls_mapping_page.dart';
import 'offline_cache_page.dart';
import '../widgets/app_icons.dart';

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

  @override
  Widget build(BuildContext context) {
    return Focus(
      onKeyEvent: (node, event) {
        if (event is KeyDownEvent && _keyBindings?.resolve(event.logicalKey) == InputAction.back) {
          Navigator.of(context).maybePop();
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: Scaffold(
        key: const Key('settings-page'),
        appBar: AppBar(title: const Text('Settings')),
        body: ListView(
          children: [
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
          ],
        ),
      ),
    );
  }
}
