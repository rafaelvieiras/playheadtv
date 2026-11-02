// Settings -> "Plugins": what this device has installed, and what the backend's
// registry offers. A channel whose source comes from a plugin cannot play (and
// is not shown) until its plugin is installed here, so this page is the only
// place that can turn such a channel on.
//
// Six buttons, no exceptions: Up/Down move the focus, Select installs a missing
// plugin or opens the dialog of an installed one (turn it off, remove it, or
// cancel), Back leaves. Installing a plugin archive is tens of megabytes, so the
// row reports progress as it downloads.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../cache/offline_runtime.dart';
import '../input/input_action.dart';
import '../plugin/plugin_installer.dart';
import '../plugin/plugin_manifest.dart';
import '../widgets/app_icons.dart';
import '../ui/ui_scale.dart';

/// The one line a plugin row shows under its name.
///
/// Kept as a pure function because it is where every state a plugin can be in
/// has to read sensibly, and a widget test per state would say less than this
/// does.
String pluginStateLabel({
  required String id,
  InstalledPluginRecord? installed,
  PluginOffering? offering,
  required Map<String, String> platformHas,
  int? progressPercent,
  String? error,
}) {
  if (error != null) return error;
  if (progressPercent != null) return 'Downloading... $progressPercent%';

  final local = installed;
  if (local != null) {
    final state = local.enabled ? 'on' : 'turned off';
    final name = local.name.isEmpty ? local.id : local.name;
    return 'Installed $name ${local.version} ($state)';
  }
  if (offering == null) {
    return 'Not offered by this backend';
  }
  final available = platformHas[id] ?? '';
  if (available.isEmpty) {
    return 'No build for this device';
  }
  return 'Not installed (version $available available)';
}

class PluginsPage extends StatefulWidget {
  const PluginsPage({super.key});

  @override
  State<PluginsPage> createState() => _PluginsPageState();
}

class _PluginsPageState extends State<PluginsPage> {
  OfflineRuntime? _runtime;
  KeyBindingsService? _keyBindings;
  List<InstalledPluginRecord> _installed = const [];
  PluginRegistry? _registry;
  String? _registryError;
  final _progress = <String, int>{};
  final _errors = <String, String>{};
  final _platformHas = <String, String>{};

  @override
  void initState() {
    super.initState();
    unawaited(_bootstrap());
  }

  Future<void> _bootstrap() async {
    final runtime = await OfflineRuntime.ensure();
    if (!mounted) return;
    setState(() {
      _runtime = runtime;
      _keyBindings = KeyBindingsService(runtime.settings);
    });
    await _reload();
  }

  /// Reads this device's plugins and the backend's registry together: the page
  /// is a comparison of the two, so one without the other says nothing.
  Future<void> _reload() async {
    final runtime = _runtime;
    if (runtime == null) return;
    // What is installed decides which channels this device can play, so the
    // answer every other screen filters with is recomputed here, right after a
    // change on this page.
    await runtime.refreshAvailability();
    final installed = await runtime.plugins.installed();
    final platform = await runtime.plugins.installer.platform();
    PluginRegistry? registry;
    String? error;
    try {
      registry = await runtime.plugins.registry(refresh: true);
    } on Object {
      // A backend that is down still lets the page show what is installed and
      // let the viewer turn it off or remove it.
      error = 'The backend cannot be reached, so no plugin can be installed right now.';
      registry = null;
    }
    final has = <String, String>{};
    for (final offering in registry?.plugins ?? const <PluginOffering>[]) {
      final release = offering.latestFor(platform);
      if (release != null) has[offering.id] = release.version;
    }
    if (!mounted) return;
    setState(() {
      _installed = installed;
      _registry = registry;
      _registryError = error;
      _platformHas
        ..clear()
        ..addAll(has);
    });
  }

  Future<void> _install(String id) async {
    final runtime = _runtime!;
    setState(() {
      _errors.remove(id);
      _progress[id] = 0;
    });
    try {
      await runtime.plugins.install(
        id,
        onProgress: (received, total) {
          if (!mounted) return;
          final percent = total <= 0 ? null : (received * 100 ~/ total).clamp(0, 100);
          if (percent == null) return;
          // Only redraw on a whole percent: this fires per chunk of a 70 MB
          // archive, and a setState per chunk would crowd out the download.
          if (_progress[id] == percent) return;
          setState(() => _progress[id] = percent);
        },
      );
    } on Object catch (e) {
      if (mounted) setState(() => _errors[id] = 'Install failed: ${_shortError(e)}');
    } finally {
      if (mounted) setState(() => _progress.remove(id));
      await _reload();
    }
  }

  Future<void> _setEnabled(InstalledPluginRecord record, bool enabled) async {
    await _runtime!.plugins.setEnabled(record.id, enabled);
    await _reload();
  }

  Future<void> _remove(InstalledPluginRecord record) async {
    await _runtime!.plugins.remove(record.id);
    await _reload();
  }

  /// What an installed plugin offers, behind a dialog so both actions stay
  /// reachable with the six buttons.
  Future<void> _openInstalled(InstalledPluginRecord record) async {
    final action = await showDialog<String>(
      context: context,
      builder: (context) => UiScaled(
        child: AlertDialog(
          key: const Key('plugin-actions-dialog'),
          title: Text(record.name.isEmpty ? record.id : record.name),
          content: Text(
            record.enabled
                ? 'Installed version ${record.version}.\n\nA channel that uses this plugin plays only while it is turned on.'
                : 'Installed version ${record.version}, currently turned off.\n\nA channel that uses this plugin does not appear while it is off.',
          ),
          actions: [
            TextButton(
              key: const Key('plugin-actions-toggle'),
              autofocus: true,
              onPressed: () => Navigator.of(context).pop(record.enabled ? 'off' : 'on'),
              child: Text(record.enabled ? 'Turn off' : 'Turn on'),
            ),
            TextButton(
              key: const Key('plugin-actions-remove'),
              onPressed: () => Navigator.of(context).pop('remove'),
              child: const Text('Remove'),
            ),
            TextButton(
              key: const Key('plugin-actions-cancel'),
              onPressed: () => Navigator.of(context).pop(),
              child: const Text('Cancel'),
            ),
          ],
        ),
      ),
    );
    if (!mounted || action == null) return;
    switch (action) {
      case 'on':
        await _setEnabled(record, true);
      case 'off':
        await _setEnabled(record, false);
      case 'remove':
        await _remove(record);
    }
  }

  static String _shortError(Object error) {
    final text = '$error';
    return text.length <= 140 ? text : '${text.substring(0, 140)}...';
  }

  @override
  Widget build(BuildContext context) => UiScaled(child: _buildPage(context));

  Widget _buildPage(BuildContext context) {
    final runtime = _runtime;
    if (runtime == null) {
      return const Scaffold(
        key: Key('plugins-page'),
        body: Center(child: CircularProgressIndicator()),
      );
    }
    final byId = {for (final record in _installed) record.id: record};
    final offerings = _registry?.plugins ?? const <PluginOffering>[];
    final knownIds = <String>{...offerings.map((o) => o.id), ...byId.keys};

    final rows = <Widget>[];
    for (final id in knownIds.toList()..sort()) {
      final offering = offerings.where((o) => o.id == id).firstOrNull;
      final record = byId[id];
      rows.add(ListTile(
        key: Key('plugin-tile-$id'),
        autofocus: rows.isEmpty,
        leading: Icon(record == null ? AppIcons.pluginMissing : (record.enabled ? AppIcons.plugin : AppIcons.pluginOff)),
        title: Text(offering?.name.isNotEmpty == true ? offering!.name : (record?.name.isNotEmpty == true ? record!.name : id)),
        subtitle: Text(
          pluginStateLabel(
            id: id,
            installed: record,
            offering: offering,
            platformHas: _platformHas,
            progressPercent: _progress[id],
            error: _errors[id],
          ),
          key: Key('plugin-state-$id'),
        ),
        onTap: _progress.containsKey(id)
            ? null
            : (record != null
                ? () => unawaited(_openInstalled(record))
                // Only an offered plugin with a build for this device can be
                // installed; tapping a row that cannot do anything is worse
                // than the row saying why.
                : (_platformHas.containsKey(id) ? () => unawaited(_install(id)) : null)),
      ));
    }

    return Focus(
      canRequestFocus: false,
      skipTraversal: true,
      onKeyEvent: (node, event) {
        if (event is KeyDownEvent && _keyBindings!.resolve(event.logicalKey) == InputAction.back) {
          Navigator.of(context).maybePop();
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: Scaffold(
        key: const Key('plugins-page'),
        appBar: AppBar(title: const Text('Plugins'), toolbarHeight: context.ui(kToolbarHeight)),
        body: ListView(
          children: [
            if (_registryError != null)
              ListTile(
                key: const Key('plugins-registry-error'),
                leading: const Icon(AppIcons.pluginMissing),
                title: const Text('The backend cannot be reached'),
                subtitle: Text(_registryError!),
              ),
            if (_registry != null && !_registry!.registryConfigured)
              ListTile(
                key: const Key('plugins-no-registry'),
                leading: const Icon(AppIcons.pluginMissing),
                title: const Text('This backend has no plugin registry'),
                subtitle: const Text(
                  'Channels whose content comes from a plugin cannot be installed here until the backend is given one.',
                ),
              ),
            if (rows.isEmpty && _registryError == null)
              ListTile(
                key: const Key('plugins-empty'),
                leading: const Icon(AppIcons.plugin),
                title: const Text('No plugins'),
                subtitle: const Text(
                  'A plugin is needed only for a channel whose content comes from outside the media server. Such a channel appears once its plugin is installed.',
                ),
              ),
            ...rows,
          ],
        ),
      ),
    );
  }
}
