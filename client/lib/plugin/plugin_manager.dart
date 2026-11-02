// What the client does with plugins, in one place: ask the backend's registry
// mirror, install what is missing, and resolve a guide item into something the
// player can open.
//
// Two installations exist and they are not the same thing: the backend installs
// a plugin to list a source's catalog while building the guide, and this client
// installs it to resolve playback at the moment a viewer tunes in. Neither
// trusts the other's copy — each verifies its own archive — so nothing here
// reads the backend's `installed` list for decisions, only for display.
import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import '../playhead_api.dart';
import 'plugin_installer.dart';
import 'plugin_manifest.dart';
import 'plugin_process.dart';

/// Why a channel cannot be played here yet.
class MissingPlugin {
  final String id;
  final String? minVersion;

  /// What is installed locally, when something is.
  final String? installedVersion;

  /// True when the plugin is installed but switched off in Settings.
  final bool disabled;

  /// True when the registry cannot run this plugin on this platform at all
  /// (`os`/`arch` with no build), which no amount of retrying fixes.
  final bool unsupportedHere;

  const MissingPlugin({
    required this.id,
    this.minVersion,
    this.installedVersion,
    this.disabled = false,
    this.unsupportedHere = false,
  });

  /// The plugin is present but older than the guide requires.
  bool get tooOld {
    final have = installedVersion;
    final want = minVersion;
    if (have == null || want == null) return false;
    return comparePluginVersions(have, want) < 0;
  }

  /// One line for Settings and for the "why is this channel missing" notice.
  String describe() {
    if (disabled) return 'The "$id" plugin is installed but turned off.';
    if (tooOld) return 'The "$id" plugin needs to be updated (version $minVersion or newer).';
    if (installedVersion != null) return 'The "$id" plugin is installed but cannot be used.';
    if (unsupportedHere) return 'The "$id" plugin has no build for this device.';
    return 'The "$id" plugin is not installed.';
  }
}

/// The plugin manager the rest of the app talks to.
///
/// It owns the running plugin processes, so a caller never has to think about
/// spawn, lifetime or shutdown: ask for a resolve and it does what it takes.
class PluginManager {
  final Uri backend;
  final Map<String, String> Function() authHeaders;
  final PluginInstaller installer;

  /// How long a registry answer is reused: a Settings page that rebuilds twice
  /// must not re-download the index, and a fresh install forces a refresh.
  final Duration registryTtl;

  final http.Client _client;
  PluginRegistry? _registry;
  DateTime? _registryAt;
  final _processes = <String, PluginProcess>{};
  final _starting = <String, Future<PluginProcess>>{};

  PluginManager({
    required this.backend,
    required this.installer,
    Map<String, String> Function()? authHeaders,
    this.registryTtl = const Duration(minutes: 5),
    http.Client? client,
  })  : authHeaders = authHeaders ?? (() => const {}),
        _client = client ?? http.Client();

  /// The registry as the backend mirrors it.
  Future<PluginRegistry> registry({bool refresh = false}) async {
    final cached = _registry;
    final at = _registryAt;
    if (!refresh && cached != null && at != null && DateTime.now().difference(at) < registryTtl) {
      return cached;
    }
    final response = await _getJson(Uri.parse('$backend/plugins'));
    final parsed = PluginRegistry.fromJson(response);
    _registry = parsed;
    _registryAt = DateTime.now();
    return parsed;
  }

  /// What is installed on this device.
  Future<List<InstalledPluginRecord>> installed() => installer.installed();

  Future<InstalledPluginRecord?> installedRecord(String id) => installer.byId(id);

  /// Installs the newest version of [id] that this device can run.
  ///
  /// Throws [PluginFormatException] when the registry offers nothing usable
  /// here, and [PluginInstallException] (from the installer) when the download
  /// or the archive does not check out. A failed install leaves whatever was
  /// installed before in place.
  Future<InstalledPluginRecord> install(String id, {void Function(int received, int total)? onProgress}) async {
    final registry = await this.registry(refresh: true);
    final offering = registry.offeringById(id);
    if (offering == null) {
      throw PluginFormatException(
        registry.registryConfigured
            ? 'the registry does not offer a "$id" plugin'
            : 'this backend has no plugin registry configured',
      );
    }
    final platform = await installer.platform();
    final release = offering.latestFor(platform);
    if (release == null) {
      throw PluginFormatException(
        'the "$id" plugin has no build for ${platform.os}-${platform.arch}',
      );
    }
    final artifact = release.forPlatform(platform)!;
    final record = await installer.install(
      offering: offering,
      release: release,
      artifact: artifact,
      backend: backend,
      headers: authHeaders(),
      onProgress: onProgress,
    );
    // A new build replaces the running process on the next resolve.
    await _stopProcess(id);
    return record;
  }

  Future<void> remove(String id) async {
    await _stopProcess(id);
    await installer.remove(id);
  }

  Future<InstalledPluginRecord?> setEnabled(String id, bool enabled) async {
    if (!enabled) await _stopProcess(id);
    return installer.setEnabled(id, enabled);
  }

  /// Every plugin a channel's entries need that this device cannot currently
  /// use. Empty means the channel can be offered.
  ///
  /// A requirement with no version in the guide is satisfied by any installed
  /// version; a requirement newer than what is installed is not.
  Future<List<MissingPlugin>> missingFor(Iterable<EpgRequirement> requirements) async {
    final wanted = <String, EpgRequirement>{};
    for (final requirement in requirements) {
      final existing = wanted[requirement.id];
      // Several entries may name the same plugin with different minimums: the
      // strictest one wins.
      if (existing == null ||
          (requirement.minVersion != null &&
              (existing.minVersion == null ||
                  comparePluginVersions(requirement.minVersion!, existing.minVersion!) > 0))) {
        wanted[requirement.id] = requirement;
      }
    }
    if (wanted.isEmpty) return const [];

    final local = {for (final record in await installer.installed()) record.id: record};
    PluginRegistry? registry;
    final missing = <MissingPlugin>[];

    for (final entry in wanted.entries) {
      final id = entry.key;
      final requirement = entry.value;
      final record = local[id];

      if (record != null) {
        if (!record.enabled) {
          missing.add(MissingPlugin(id: id, minVersion: requirement.minVersion, installedVersion: record.version, disabled: true));
          continue;
        }
        final min = requirement.minVersion;
        if (min != null && comparePluginVersions(record.version, min) < 0) {
          missing.add(MissingPlugin(id: id, minVersion: min, installedVersion: record.version));
        }
        continue;
      }

      // Not installed: only worth reporting as unsupported when we actually know
      // the registry cannot serve this device, which needs a (cached) lookup.
      bool unsupported = false;
      try {
        registry ??= await this.registry();
        final offering = registry.offeringById(id);
        if (offering != null) {
          final platform = await installer.platform();
          unsupported = offering.latestFor(platform) == null;
        }
      } on Object {
        // An unreachable backend must not turn "not installed" into a different
        // answer; the plugin is missing either way.
      }
      missing.add(MissingPlugin(
        id: id,
        minVersion: requirement.minVersion,
        unsupportedHere: unsupported,
      ));
    }
    return missing;
  }

  /// Resolves one guide item into something the player can open.
  ///
  /// [requirement] comes from the entry itself (`EpgEntry.requires`), so the
  /// config the source was created with travels with the guide and nothing has
  /// to be looked up here.
  Future<PluginResolution> resolve({
    required EpgRequirement requirement,
    required String externalId,
    Map<String, dynamic> config = const {},
    int? durationMs,
    int startAtMs = 0,
    List<String> modes = const ['single', 'pair'],
  }) async {
    final record = await installer.byId(requirement.id);
    if (record == null) {
      throw PluginProcessException('the "${requirement.id}" plugin is not installed');
    }
    if (!record.enabled) {
      throw PluginProcessException('the "${requirement.id}" plugin is turned off');
    }
    final minimum = requirement.minVersion;
    if (minimum != null && comparePluginVersions(record.version, minimum) < 0) {
      throw PluginProcessException(
        'the "${requirement.id}" plugin is ${record.version}, this channel needs $minimum',
      );
    }

    final process = await _processFor(record);
    return process.resolve(
      config: config,
      externalId: externalId,
      durationMs: durationMs,
      startAtMs: startAtMs,
      modes: modes,
    );
  }

  /// One running process per plugin, started on demand.
  Future<PluginProcess> _processFor(InstalledPluginRecord record) async {
    final running = _processes[record.id];
    if (running != null && running.isRunning) return running;
    final starting = _starting[record.id];
    if (starting != null) return starting;

    final process = PluginProcess(record);
    _processes[record.id] = process;
    final future = process.start().then((_) => process).whenComplete(() {
      _starting.remove(record.id);
    });
    _starting[record.id] = future;
    try {
      return await future;
    } on Object {
      _processes.remove(record.id);
      rethrow;
    }
  }

  Future<void> _stopProcess(String id) async {
    final process = _processes.remove(id);
    if (process != null) await process.close();
  }

  /// Stops every running plugin. Called when the player tears down.
  Future<void> close() async {
    final processes = _processes.values.toList(growable: false);
    _processes.clear();
    _starting.clear();
    await Future.wait(processes.map((process) => process.close()));
  }

  Future<dynamic> _getJson(Uri uri) async {
    final http.Response response;
    try {
      response = await _client.get(uri, headers: authHeaders()).timeout(const Duration(seconds: 20));
    } on Object catch (error) {
      throw PlayheadConnectionException(uri, error);
    }
    if (response.statusCode != 200) {
      throw PlayheadHttpException(uri, response.statusCode, response.body);
    }
    try {
      return jsonDecode(response.body);
    } on Object catch (error) {
      throw PluginFormatException('GET /plugins answered something that is not JSON: $error');
    }
  }
}
