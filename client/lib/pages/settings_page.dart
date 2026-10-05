// Settings screen: list of settings sections — "Remap controls" (see
// controls_mapping_page.dart) and "Offline cache" (see offline_cache_page.dart).
//
// Operable with the six baseline buttons: Up/Down move the focus between the
// rows, Select opens one and Back (Esc by default) returns to the mosaic.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../input/input_action.dart';
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

  @override
  void initState() {
    super.initState();
    unawaited(SettingsService.create().then((settings) {
      if (mounted) _keyBindings = KeyBindingsService(settings);
    }));
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
          ],
        ),
      ),
    );
  }
}
