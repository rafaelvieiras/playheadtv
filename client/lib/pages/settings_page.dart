// Settings screen: list of settings sections. For now only 1 item —
// "Remap controls", which navigates to ControlsMappingPage (see
// controls_mapping_page.dart).
import 'package:flutter/material.dart';

import 'controls_mapping_page.dart';
import '../widgets/app_icons.dart';

class SettingsPage extends StatelessWidget {
  const SettingsPage({super.key});

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      key: const Key('settings-page'),
      appBar: AppBar(title: const Text('Settings')),
      body: ListView(
        children: [
          ListTile(
            key: const Key('settings-controls-tile'),
            leading: const Icon(AppIcons.controls),
            title: const Text('Remap controls'),
            onTap: () {
              Navigator.of(context).push(MaterialPageRoute(builder: (_) => const ControlsMappingPage()));
            },
          ),
        ],
      ),
    );
  }
}
