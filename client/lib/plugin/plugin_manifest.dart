// What a plugin is, as the backend's registry mirror describes it
// (docs/PLUGINS.md in the repository, protocol 1).
//
// The client never fetches the upstream registry itself: GET /plugins on the
// backend is a cached mirror whose artifact URLs already point back at it, so a
// client that can reach its backend can install a plugin even when it cannot
// reach the internet.

/// Raised when a registry document, a manifest or an installation is malformed.
/// A malformed registry must never be half-applied: the caller gets this and
/// keeps whatever was installed before.
class PluginFormatException implements Exception {
  final String message;
  const PluginFormatException(this.message);
  @override
  String toString() => 'PluginFormatException: $message';
}

/// The manifest a plugin ships as `plugin.json`, and reports from `info`.
class PluginManifest {
  final String id;
  final String name;
  final String description;
  final String version;
  final int protocol;
  final String entrypoint;
  final List<String> capabilities;

  const PluginManifest({
    required this.id,
    required this.name,
    required this.version,
    required this.protocol,
    required this.entrypoint,
    required this.capabilities,
    this.description = '',
  });

  static final _idPattern = RegExp(r'^[a-z0-9][a-z0-9-]*$');
  static final _versionPattern = RegExp(r'^[A-Za-z0-9._+-]+$');

  factory PluginManifest.fromJson(Object? json) {
    if (json is! Map<String, dynamic>) {
      throw const PluginFormatException('manifest is not a JSON object');
    }
    return PluginManifest(
      id: _string(json['id'], 'id'),
      name: _string(json['name'], 'name'),
      description: json['description'] is String ? json['description'] as String : '',
      version: _string(json['version'], 'version'),
      protocol: _int(json['protocol'], 'protocol'),
      entrypoint: _string(json['entrypoint'], 'entrypoint'),
      capabilities: _stringList(json['capabilities'], 'capabilities'),
    );
  }

  /// Rejects a manifest the client cannot run or must not trust.
  void validate({int requiredProtocol = pluginProtocol}) {
    if (!_idPattern.hasMatch(id)) {
      throw PluginFormatException('id "$id" is not lowercase [a-z0-9-]');
    }
    if (name.trim().isEmpty) throw const PluginFormatException('name is empty');
    if (!_versionPattern.hasMatch(version)) {
      throw PluginFormatException('version "$version" is not an orderable version');
    }
    if (protocol < 1) throw PluginFormatException('protocol $protocol is not a version');
    if (protocol > requiredProtocol) {
      // A plugin newer than the client is fine as long as it speaks a protocol
      // this client knows; it said so with its own number.
      throw PluginFormatException('protocol $protocol is newer than $requiredProtocol');
    }
    if (entrypoint.isEmpty || entrypoint.startsWith('/') || entrypoint.contains('..')) {
      throw PluginFormatException('entrypoint "$entrypoint" must be a relative path inside the artifact');
    }
    if (capabilities.isEmpty) throw const PluginFormatException('no capabilities');
    for (final capability in capabilities) {
      if (!pluginCapabilities.contains(capability)) {
        throw PluginFormatException('unknown capability "$capability"');
      }
    }
  }

  bool has(String capability) => capabilities.contains(capability);

  Map<String, dynamic> toJson() => {
        'id': id,
        'name': name,
        if (description.isNotEmpty) 'description': description,
        'version': version,
        'protocol': protocol,
        'entrypoint': entrypoint,
        'capabilities': capabilities,
      };
}

/// Protocol this client speaks.
const pluginProtocol = 1;

/// Capabilities a manifest may declare.
const pluginCapabilities = {'catalog.list', 'media.resolve'};

/// One downloadable archive.
class PluginArtifact {
  final String os;
  final String arch;
  final String url;
  final String sha256;
  final int size;

  const PluginArtifact({
    required this.os,
    required this.arch,
    required this.url,
    required this.sha256,
    required this.size,
  });

  factory PluginArtifact.fromJson(Object? json) {
    if (json is! Map<String, dynamic>) {
      throw const PluginFormatException('artifact is not a JSON object');
    }
    final url = _string(json['url'], 'artifact url');
    // The backend rewrites artifact urls to a path on itself, so both an
    // absolute url and a server-relative path are valid here; anything else
    // would be resolved against nothing.
    final absolute = url.startsWith('http://') || url.startsWith('https://');
    if (!absolute && !url.startsWith('/')) {
      throw PluginFormatException('artifact url "$url" is neither absolute nor a server path');
    }
    final sha = _string(json['sha256'], 'artifact sha256').toLowerCase();
    if (!RegExp(r'^[0-9a-f]{64}$').hasMatch(sha)) {
      // An artifact with no hash is never installed: the hash is the identity.
      throw PluginFormatException('artifact sha256 "$sha" is not 64 hex characters');
    }
    return PluginArtifact(
      os: _string(json['os'], 'artifact os'),
      arch: _string(json['arch'], 'artifact arch'),
      url: url,
      sha256: sha,
      size: json['size'] is num ? (json['size'] as num).toInt() : 0,
    );
  }

  /// The URL to actually fetch: the backend rewrites it to a path on itself.
  Uri resolveAgainst(Uri backend) =>
      url.startsWith('http://') || url.startsWith('https://') ? Uri.parse(url) : backend.resolve(url);
}

/// One released version of a plugin.
class PluginRelease {
  final String version;
  final int protocol;
  final List<PluginArtifact> artifacts;

  const PluginRelease({required this.version, required this.protocol, required this.artifacts});

  factory PluginRelease.fromJson(Object? json) {
    if (json is! Map<String, dynamic>) {
      throw const PluginFormatException('release is not a JSON object');
    }
    final artifacts = (json['artifacts'] as List? ?? const [])
        .map(PluginArtifact.fromJson)
        .toList(growable: false);
    return PluginRelease(
      version: _string(json['version'], 'release version'),
      protocol: _int(json['protocol'], 'release protocol'),
      artifacts: artifacts,
    );
  }
}

/// A plugin the registry offers.
class PluginOffering {
  final String id;
  final String name;
  final String description;
  final List<PluginRelease> releases;

  const PluginOffering({
    required this.id,
    required this.name,
    required this.releases,
    this.description = '',
  });

  factory PluginOffering.fromJson(Object? json) {
    if (json is! Map<String, dynamic>) {
      throw const PluginFormatException('plugin is not a JSON object');
    }
    return PluginOffering(
      id: _string(json['id'], 'plugin id'),
      name: _string(json['name'], 'plugin name'),
      description: json['description'] is String ? json['description'] as String : '',
      releases: (json['versions'] as List? ?? const [])
          .map(PluginRelease.fromJson)
          .toList(growable: false),
    );
  }
}

/// A plugin this backend has installed.
/// One setting a plugin declares for the sources of it (protocol 1, `config`).
///
/// The plugin says what a source of it can be configured with, so this client can
/// build a settings screen without knowing a single one of the plugin's keys —
/// and without the core learning them either.
class PluginConfigField {
  final String key;
  final String label;

  /// `choice`, `bool`, `number` or `text`; it says how the value is written into
  /// the source's config.
  final String type;
  final String? help;

  /// The values a `choice` offers, in the order to offer them. Empty for every
  /// other type.
  final List<PluginConfigOption> options;

  const PluginConfigField({
    required this.key,
    required this.label,
    required this.type,
    this.help,
    this.options = const [],
  });

  factory PluginConfigField.fromJson(Object? json) {
    if (json is! Map<String, dynamic>) {
      throw const PluginFormatException('plugin config field is not a JSON object');
    }
    return PluginConfigField(
      key: _string(json['key'], 'config key'),
      label: json['label'] is String ? json['label'] as String : _string(json['key'], 'config key'),
      type: json['type'] is String ? json['type'] as String : 'text',
      help: json['help'] is String && (json['help'] as String).trim().isNotEmpty ? json['help'] as String : null,
      options: (json['options'] as List? ?? const [])
          .map(PluginConfigOption.fromJson)
          .toList(growable: false),
    );
  }
}

/// One value of a [PluginConfigField] of type `choice`.
class PluginConfigOption {
  /// Written into the source's config exactly as declared: a number for a
  /// numeric setting, a string for a textual one.
  final Object? value;
  final String label;

  const PluginConfigOption({required this.value, required this.label});

  factory PluginConfigOption.fromJson(Object? json) {
    if (json is! Map<String, dynamic>) {
      throw const PluginFormatException('plugin config option is not a JSON object');
    }
    return PluginConfigOption(
      value: json['value'],
      label: json['label'] is String ? json['label'] as String : '${json['value']}',
    );
  }
}

class InstalledPlugin {
  final String id;
  final String version;
  final int protocol;
  final String os;
  final String arch;
  final String capabilities;
  final bool enabled;

  /// What this plugin declares a source of it can be configured with; empty when
  /// it declares nothing (or its files are gone).
  final List<PluginConfigField> configFields;

  const InstalledPlugin({
    required this.id,
    required this.version,
    required this.protocol,
    required this.os,
    required this.arch,
    required this.capabilities,
    required this.enabled,
    this.configFields = const [],
  });

  factory InstalledPlugin.fromJson(Object? json) {
    if (json is! Map<String, dynamic>) {
      throw const PluginFormatException('installed plugin is not a JSON object');
    }
    return InstalledPlugin(
      id: _string(json['id'], 'installed id'),
      version: _string(json['version'], 'installed version'),
      protocol: _int(json['protocol'], 'installed protocol'),
      os: json['os'] is String ? json['os'] as String : '',
      arch: json['arch'] is String ? json['arch'] as String : '',
      capabilities: json['capabilities'] is String ? json['capabilities'] as String : '',
      enabled: json['enabled'] == true,
      configFields: (json['config_fields'] as List? ?? const [])
          .map(PluginConfigField.fromJson)
          .toList(growable: false),
    );
  }

  List<String> get capabilityList =>
      capabilities.split(',').map((c) => c.trim()).where((c) => c.isNotEmpty).toList(growable: false);
}

/// One plugin-backed media source, as `GET /plugin-sources` reports it: which
/// plugin lists it and the values of the settings that plugin declared.
class PluginSource {
  final String id;
  final String name;
  final String pluginId;
  final bool enabled;
  final Map<String, Object?> settings;

  const PluginSource({
    required this.id,
    required this.name,
    required this.pluginId,
    required this.enabled,
    required this.settings,
  });

  factory PluginSource.fromJson(Object? json) {
    if (json is! Map<String, dynamic>) {
      throw const PluginFormatException('plugin source is not a JSON object');
    }
    final settings = json['settings'];
    return PluginSource(
      id: _string(json['id'], 'source id'),
      name: json['name'] is String ? json['name'] as String : '',
      pluginId: json['plugin_id'] is String ? json['plugin_id'] as String : '',
      enabled: json['enabled'] == true,
      settings: settings is Map<String, dynamic> ? Map<String, Object?>.from(settings) : const {},
    );
  }

  /// The label to show for one setting: the option's label when the value is one
  /// a choice offers, the raw value otherwise.
  String settingLabel(PluginConfigField field) {
    final value = settings[field.key];
    if (value == null) return 'not set';
    for (final option in field.options) {
      if (_sameValue(option.value, value)) return option.label;
    }
    if (value is bool) return value ? 'on' : 'off';
    return '$value';
  }
}

/// JSON numbers arrive as `int` or `double` depending on who wrote them, and
/// `1080 == 1080.0` is true in Dart but `'1080' != 1080` is what a choice has to
/// compare, so the comparison is made explicit.
bool _sameValue(Object? a, Object? b) {
  if (a is num && b is num) return a.toDouble() == b.toDouble();
  return a == b;
}

/// The answer to `GET /plugins`.
class PluginRegistry {
  final bool registryConfigured;
  final List<InstalledPlugin> installed;
  final List<PluginOffering> plugins;

  const PluginRegistry({
    required this.registryConfigured,
    required this.installed,
    required this.plugins,
  });

  factory PluginRegistry.fromJson(Object? json) {
    if (json is! Map<String, dynamic>) {
      throw const PluginFormatException('registry response is not a JSON object');
    }
    return PluginRegistry(
      registryConfigured: json['registry_configured'] == true,
      installed: (json['installed'] as List? ?? const [])
          .map(InstalledPlugin.fromJson)
          .toList(growable: false),
      plugins: (json['plugins'] as List? ?? const [])
          .map(PluginOffering.fromJson)
          .toList(growable: false),
    );
  }

  InstalledPlugin? installedById(String id) {
    for (final plugin in installed) {
      if (plugin.id == id) return plugin;
    }
    return null;
  }

  PluginOffering? offeringById(String id) {
    for (final plugin in plugins) {
      if (plugin.id == id) return plugin;
    }
    return null;
  }
}

/// Compares two plugin versions, treating digit runs as numbers so
/// `2026.10.26` sorts above `2026.9.30`.
int comparePluginVersions(String a, String b) {
  final left = _versionParts(a);
  final right = _versionParts(b);
  for (var i = 0; i < left.length || i < right.length; i++) {
    final l = i < left.length ? left[i] : 0;
    final r = i < right.length ? right[i] : 0;
    if (l != r) return l < r ? -1 : 1;
  }
  return 0;
}

List<int> _versionParts(String version) => RegExp(r'\d+')
    .allMatches(version)
    .map((m) => int.parse(m.group(0)!))
    .toList(growable: false);

String _string(Object? value, String field) {
  if (value is String && value.trim().isNotEmpty) return value;
  throw PluginFormatException('"$field" is missing or not a string');
}

int _int(Object? value, String field) {
  if (value is int) return value;
  if (value is num) return value.toInt();
  throw PluginFormatException('"$field" is missing or not a number');
}

List<String> _stringList(Object? value, String field) {
  if (value is List && value.isNotEmpty) {
    return value.map((v) {
      if (v is String) return v;
      throw PluginFormatException('"$field" has a non-string entry');
    }).toList(growable: false);
  }
  throw PluginFormatException('"$field" is missing or empty');
}
