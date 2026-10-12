// Settings -> "Diagnostics": where the log file is, the log level in force, and
// an action that gathers the rotated logs into one `.txt` to attach to a bug
// report. Operable with the six baseline buttons: Up/Down move the focus,
// Select runs the action, Back leaves (see docs/ARCHITECTURE.md "Client
// logging").
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../input/input_action.dart';
import '../log/log.dart';
import '../services/settings_service.dart';
import '../ui/ui_scale.dart';
import '../update/app_update.dart';
import '../widgets/app_icons.dart';

class DiagnosticsPage extends StatefulWidget {
  const DiagnosticsPage({super.key});

  @override
  State<DiagnosticsPage> createState() => _DiagnosticsPageState();
}

class _DiagnosticsPageState extends State<DiagnosticsPage> {
  KeyBindingsService? _keyBindings;
  bool _busy = false;

  @override
  void initState() {
    super.initState();
    unawaited(SettingsService.create().then((settings) {
      if (mounted) setState(() => _keyBindings = KeyBindingsService(settings));
    }));
  }

  Future<void> _createBundle() async {
    if (_busy) return;
    setState(() => _busy = true);
    final messenger = ScaffoldMessenger.maybeOf(context);
    final bundle = await Log.exportBundle(appVersion: appVersion);
    if (!mounted) return;
    setState(() => _busy = false);
    final message = bundle == null
        ? 'No log file to bundle: logging to a file is not available here'
        : 'Log bundle saved: ${bundle.path}';
    messenger?.showSnackBar(
      SnackBar(key: const Key('diagnostics-toast'), content: Text(message), duration: const Duration(seconds: 6)),
    );
  }

  @override
  Widget build(BuildContext context) => UiScaled(child: _buildPage(context));

  Widget _buildPage(BuildContext context) {
    final file = Log.file;
    return Focus(
      onKeyEvent: (node, event) {
        if (event is KeyDownEvent && _keyBindings?.resolve(event.logicalKey) == InputAction.back) {
          Navigator.of(context).maybePop();
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: Scaffold(
        key: const Key('diagnostics-page'),
        appBar: AppBar(title: const Text('Diagnostics'), toolbarHeight: context.ui(kToolbarHeight)),
        body: ListView(
          children: [
            ListTile(
              key: const Key('diagnostics-path-tile'),
              leading: const Icon(AppIcons.diagnostics),
              title: const Text('Log file'),
              subtitle: Text(file?.path ?? 'Not written to a file in this setup', key: const Key('diagnostics-path-value')),
            ),
            ListTile(
              key: const Key('diagnostics-level-tile'),
              leading: const Icon(AppIcons.logLevel),
              title: const Text('Log level'),
              subtitle: Text(logLevelLabel(Log.level, Log.configuredLevel), key: const Key('diagnostics-level-value')),
            ),
            ListTile(
              key: const Key('diagnostics-bundle-tile'),
              autofocus: true,
              leading: const Icon(AppIcons.diagnostics),
              title: const Text('Create log bundle'),
              subtitle: const Text(
                'Gathers the log files into one .txt next to the log. Secrets are redacted; '
                'review it before sharing it with a bug report.',
              ),
              onTap: () => unawaited(_createBundle()),
            ),
          ],
        ),
      ),
    );
  }
}
