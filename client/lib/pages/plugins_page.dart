// Settings -> "Plugins": the plugins installed on the **backend**, and what its
// registry offers.
//
// A plugin runs where the content is resolved, and that is the backend: it carries
// the extractor and the remuxer, and a TV box cannot run either. The client only
// fetches the stream the backend points it at (`GET /catalog-items/{id}/play`), so
// what matters here is the backend's list, never this device's.
//
// A channel whose content comes from a plugin the backend lacks is not offered at
// all, so this page is where a plugin the backend already has gets turned on.
//
// Installing is the one thing this page cannot do: it downloads a program from the
// registry the admin configured and runs it on the backend, which the backend only
// accepts from an admin session (POST /plugins/{id}/install). A row for a plugin
// that is not installed says so, instead of offering a call that would be refused.
// Turning an installed plugin on or off, and removing it, are calls a paired device
// may make (backend/internal/api/auth.go, isPluginTogglePath).
//
// Six buttons, no exceptions: Up/Down move the focus, Select opens the installed
// plugin's dialog (turn it off, remove it, or cancel) or explains a missing one,
// Back leaves.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../cache/offline_runtime.dart';
import '../input/input_action.dart';
import '../plugin/plugin_manifest.dart';
import '../ui/ui_scale.dart';
import '../widgets/app_icons.dart';
import '../widgets/plugin_source_settings_dialog.dart';

/// The one line a plugin row shows under its name.
///
/// A pure function because it is where every state a plugin can be in has to read
/// sensibly, and a widget test per state would say less than this does.
String pluginStateLabel({
  required String id,
  String? installedVersion,
  String? installedName,
  bool enabled = true,
  String? availableVersion,
  bool offered = true,
  String? error,
}) {
  if (error != null) return error;

  final local = installedVersion;
  if (local != null) {
    final name = (installedName != null && installedName.isNotEmpty) ? installedName : id;
    final state = enabled ? 'on' : 'turned off';
    return 'Installed $name $local ($state)';
  }
  if (!offered) {
    return 'Not offered by this backend';
  }
  final available = availableVersion;
  if (available == null || available.isEmpty) {
    return 'Not installable on this backend';
  }
  return 'Not installed here (version $available offered): the admin installs it';
}

class PluginsPage extends StatefulWidget {
  const PluginsPage({super.key});

  @override
  State<PluginsPage> createState() => _PluginsPageState();
}

class _PluginsPageState extends State<PluginsPage> {
  OfflineRuntime? _runtime;
  KeyBindingsService? _keyBindings;
  PluginRegistry? _registry;
  String? _registryError;
  List<PluginSource> _sources = const [];
  String? _sourcesError;

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

  Future<void> _reload() async {
    final runtime = _runtime;
    if (runtime == null) return;
    // What the backend has installed decides which channels can be offered, so the
    // answer every other screen filters with is recomputed here, right after a
    // change on this page.
    await runtime.refreshAvailability();
    PluginRegistry? registry;
    String? error;
    try {
      registry = await runtime.api.getPlugins();
    } on Object {
      error = 'The backend cannot be reached, so its plugins cannot be read or changed right now.';
    }
    // The sources a plugin lists, with the settings its own manifest declares.
    // A backend that answers about plugins but not about sources still shows the
    // plugin rows: the two are separate reads on purpose.
    List<PluginSource> sources = const [];
    String? sourcesError;
    try {
      sources = await runtime.api.getPluginSources();
    } on Object {
      sourcesError = 'The settings of the plugin sources cannot be read right now.';
    }
    if (!mounted) return;
    setState(() {
      _registry = registry;
      _registryError = error;
      _sources = sources;
      _sourcesError = sourcesError;
    });
  }

  /// Saves one setting of one source and returns it as the backend now holds it.
  Future<PluginSource> _saveSettings(String sourceId, Map<String, Object?> settings) async {
    final runtime = _runtime!;
    final updated = await runtime.api.updatePluginSourceSettings(sourceId, settings);
    if (mounted) {
      setState(() {
        _sources = [for (final source in _sources) source.id == updated.id ? updated : source];
        _sourcesError = null;
      });
    }
    return updated;
  }

  /// One row per plugin source that has settings to show. A source whose plugin
  /// declares nothing has nothing to configure here, so it is not listed: an
  /// empty screen is worse than no screen.
  List<Widget> _sourceRows(Map<String, InstalledPlugin> installed) {
    final rows = <Widget>[];
    for (final source in _sources) {
      final fields = installed[source.pluginId]?.configFields ?? const <PluginConfigField>[];
      if (fields.isEmpty) continue;
      rows.add(ListTile(
        key: Key('plugin-source-${source.id}'),
        leading: const Icon(AppIcons.plugin),
        title: Text(source.name.isEmpty ? source.id : source.name),
        subtitle: Text(
          [for (final field in fields) '${field.label} ${source.settingLabel(field)}'].join(' · '),
          key: Key('plugin-source-values-${source.id}'),
        ),
        onTap: () => _openSourceSettings(source, fields),
      ));
    }
    return rows;
  }

  void _openSourceSettings(PluginSource source, List<PluginConfigField> fields) {
    final keyBindings = _keyBindings;
    if (keyBindings == null) return;
    unawaited(showPluginSourceSettings(
      context,
      source: source,
      fields: fields,
      save: (settings) => _saveSettings(source.id, settings),
      keyBindings: keyBindings,
    ));
  }

  /// Why a plugin this backend's registry offers is not installed here, and who
  /// installs it. Reached only by Select on that row, so it costs nothing until
  /// someone asks.
  Future<void> _explainMissing(PluginOffering offering) async {
    final latest = offering.releases.map((r) => r.version).fold<String?>(null, (best, version) {
          if (best == null) return version;
          return comparePluginVersions(version, best) > 0 ? version : best;
        });
    final version = (latest == null || latest.isEmpty) ? '' : ' version $latest';
    final name = offering.name.isNotEmpty ? offering.name : offering.id;
    await showDialog<void>(
      context: context,
      builder: (context) => UiScaled(
        child: AlertDialog(
          key: const Key('plugin-missing-dialog'),
          title: Text(name),
          content: Text(
            'This backend offers $name$version but has not installed it, so a channel '
            'that needs it does not appear here.\n\n'
            'Installing puts the plugin on the backend, so only the admin can do it, '
            'from the admin web page. After that it shows up in this list and can be '
            'turned on and off with this remote.',
          ),
          actions: [
            TextButton(
              key: const Key('plugin-missing-close'),
              autofocus: true,
              onPressed: () => Navigator.of(context).pop(),
              child: const Text('Close'),
            ),
          ],
        ),
      ),
    );
  }

  Future<void> _setEnabled(String id, bool enabled) async {
    await _runtime!.api.setPluginEnabled(id, enabled);
    await _reload();
  }

  Future<void> _remove(String id) async {
    await _runtime!.api.deletePlugin(id);
    await _reload();
  }

  /// What an installed plugin offers, behind a dialog so both actions stay
  /// reachable with the six buttons.
  Future<void> _openInstalled(InstalledPlugin plugin, String? displayName) async {
    final action = await showDialog<String>(
      context: context,
      builder: (context) => UiScaled(
        child: AlertDialog(
          key: const Key('plugin-actions-dialog'),
          title: Text(displayName?.isNotEmpty == true ? displayName! : plugin.id),
          content: Text(
            plugin.enabled
                ? 'Installed on the backend, version ${plugin.version}.\n\n'
                    'A channel that uses this plugin plays only while it is turned on.'
                : 'Installed on the backend, version ${plugin.version}, currently turned off.\n\n'
                    'A channel that uses this plugin does not appear while it is off.',
          ),
          actions: [
            TextButton(
              key: const Key('plugin-actions-toggle'),
              autofocus: true,
              onPressed: () => Navigator.of(context).pop(plugin.enabled ? 'off' : 'on'),
              child: Text(plugin.enabled ? 'Turn off' : 'Turn on'),
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
        await _setEnabled(plugin.id, true);
      case 'off':
        await _setEnabled(plugin.id, false);
      case 'remove':
        await _remove(plugin.id);
    }
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
    final registry = _registry;
    final installed = {for (final plugin in registry?.installed ?? const <InstalledPlugin>[]) plugin.id: plugin};
    final offerings = {for (final plugin in registry?.plugins ?? const <PluginOffering>[]) plugin.id: plugin};
    final knownIds = <String>{...installed.keys, ...offerings.keys}..toList().sort();

    final rows = <Widget>[];
    for (final id in knownIds) {
      final plugin = installed[id];
      final offering = offerings[id];
      // The newest release the backend can install, for the row's hint.
      final latest = offering?.releases.map((r) => r.version).fold<String?>(null, (best, version) {
            if (best == null) return version;
            return comparePluginVersions(version, best) > 0 ? version : best;
          });
      rows.add(ListTile(
        key: Key('plugin-tile-$id'),
        autofocus: rows.isEmpty,
        leading: Icon(plugin == null
            ? AppIcons.pluginMissing
            : (plugin.enabled ? AppIcons.plugin : AppIcons.pluginOff)),
        title: Text(offering?.name.isNotEmpty == true ? offering!.name : (plugin?.id ?? id)),
        subtitle: Text(
          pluginStateLabel(
            id: id,
            installedVersion: plugin?.version,
            installedName: offering?.name,
            enabled: plugin?.enabled ?? true,
            availableVersion: latest,
            offered: offering != null,
          ),
          key: Key('plugin-state-$id'),
        ),
        onTap: plugin != null
            ? () => unawaited(_openInstalled(plugin, offering?.name))
            // A plugin the backend offers but has not installed cannot be
            // installed from here; the row explains where it happens.
            : (offering != null ? () => unawaited(_explainMissing(offering)) : null),
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
            if (registry != null && !registry.registryConfigured)
              ListTile(
                key: const Key('plugins-no-registry'),
                leading: const Icon(AppIcons.pluginMissing),
                title: const Text('This backend has no plugin registry'),
                subtitle: const Text(
                  'Channels whose content comes from a plugin cannot be installed until the backend is given one.',
                ),
              ),
            if (rows.isEmpty && _registryError == null)
              ListTile(
                key: const Key('plugins-empty'),
                leading: const Icon(AppIcons.plugin),
                title: const Text('No plugins'),
                subtitle: const Text(
                  'A plugin is needed only for a channel whose content comes from outside the media server. '
                  'Such a channel appears once its plugin is installed here.',
                ),
              ),
            ...rows,
            if (_sourcesError != null)
              ListTile(
                key: const Key('plugin-sources-error'),
                leading: const Icon(AppIcons.pluginMissing),
                title: Text(_sourcesError!),
              ),
            if (_sourceRows(installed).isNotEmpty) ...[
              ListTile(
                dense: true,
                title: Text('Source settings', style: Theme.of(context).textTheme.labelLarge),
              ),
              ..._sourceRows(installed),
            ],
          ],
        ),
      ),
    );
  }
}
