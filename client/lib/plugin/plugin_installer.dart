// Installing a plugin on the client: download the archive the backend mirrors,
// verify its SHA-256, extract it under the app's own directory, and record what
// was installed.
//
// Everything here is pure Dart (no Flutter, no platform channels) so the whole
// install path can be unit tested: give it a directory and an http.Client.
//
// The archive is the identity. A plugin is installed under a staging directory,
// checked against the manifest it ships, and only then moved into place, so a
// failed or mismatched download can never leave a half-installed plugin that the
// player would try to run.
import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:crypto/crypto.dart';
import 'package:http/http.dart' as http;

import 'plugin_manifest.dart';

/// Cap on a downloaded archive; matches the backend's own bound.
const maxPluginArtifactBytes = 512 * 1024 * 1024;

/// Cap on what one archive may expand to.
const maxPluginExtractedBytes = 1024 * 1024 * 1024;

/// Cap on entries in one archive.
const maxPluginEntries = 20000;

/// What is on disk for one installed plugin.
class InstalledPluginRecord {
  final String id;
  final String version;
  final int protocol;
  final String name;
  final String description;
  final String entrypoint;
  final List<String> capabilities;
  final String sha256;
  final String os;
  final String arch;
  final bool enabled;

  /// Directory the archive was extracted into.
  final Directory directory;

  const InstalledPluginRecord({
    required this.id,
    required this.version,
    required this.protocol,
    required this.name,
    required this.description,
    required this.entrypoint,
    required this.capabilities,
    required this.sha256,
    required this.os,
    required this.arch,
    required this.enabled,
    required this.directory,
  });

  File get executable => File('${directory.path}${Platform.pathSeparator}$entrypoint');

  bool has(String capability) => capabilities.contains(capability);

  InstalledPluginRecord copyWith({bool? enabled}) => InstalledPluginRecord(
        id: id,
        version: version,
        protocol: protocol,
        name: name,
        description: description,
        entrypoint: entrypoint,
        capabilities: capabilities,
        sha256: sha256,
        os: os,
        arch: arch,
        enabled: enabled ?? this.enabled,
        directory: directory,
      );

  factory InstalledPluginRecord.fromJson(Map<String, dynamic> json, Directory directory) {
    return InstalledPluginRecord(
      id: json['id'] as String,
      version: json['version'] as String,
      protocol: json['protocol'] as int,
      name: json['name'] as String? ?? json['id'] as String,
      description: json['description'] as String? ?? '',
      entrypoint: json['entrypoint'] as String,
      capabilities: (json['capabilities'] as List? ?? const []).cast<String>(),
      sha256: json['sha256'] as String? ?? '',
      os: json['os'] as String? ?? '',
      arch: json['arch'] as String? ?? '',
      // Absent means enabled: a record written by an older build had no flag.
      enabled: json['enabled'] as bool? ?? true,
      directory: directory,
    );
  }

  Map<String, dynamic> toJson() => {
        'id': id,
        'version': version,
        'protocol': protocol,
        'name': name,
        'description': description,
        'entrypoint': entrypoint,
        'capabilities': capabilities,
        'sha256': sha256,
        'os': os,
        'arch': arch,
        'enabled': enabled,
      };
}

/// Installs, lists, enables and removes plugins in one directory.
class PluginInstaller {
  /// Where plugins live: one subdirectory per plugin id.
  final Directory root;
  final http.Client _client;
  final Duration timeout;
  final Future<PluginPlatform> Function() _platform;

  PluginInstaller({
    required this.root,
    http.Client? client,
    this.timeout = const Duration(minutes: 5),
    Future<PluginPlatform> Function()? platform,
  })  : _client = client ?? http.Client(),
        _platform = platform ?? hostPlatform;

  /// What is on disk, most recently installed first.
  Future<List<InstalledPluginRecord>> installed() async {
    if (!await root.exists()) return const [];
    final records = <InstalledPluginRecord>[];
    await for (final entry in root.list()) {
      if (entry is! Directory) continue;
      if (entry.path.split(Platform.pathSeparator).last.startsWith('.')) continue;
      final record = await _read(entry);
      if (record != null) records.add(record);
    }
    records.sort((a, b) => a.id.compareTo(b.id));
    return records;
  }

  Future<InstalledPluginRecord?> byId(String id) async {
    final dir = _pluginDir(id);
    if (!await dir.exists()) return null;
    return _read(dir);
  }

  Future<InstalledPluginRecord?> _read(Directory dir) async {
    final meta = File('${dir.path}${Platform.pathSeparator}installed.json');
    if (!await meta.exists()) return null;
    try {
      final json = jsonDecode(await meta.readAsString());
      if (json is! Map<String, dynamic>) return null;
      return InstalledPluginRecord.fromJson(json, dir);
    } on Object {
      // A record that cannot be read is treated as "not installed": the plugin
      // is reinstalled, never run on a guess.
      return null;
    }
  }

  Directory _pluginDir(String id) => Directory('${root.path}${Platform.pathSeparator}$id');

  /// Downloads, verifies and installs one release. Returns the record on disk.
  Future<InstalledPluginRecord> install({
    required PluginOffering offering,
    required PluginRelease release,
    required PluginArtifact artifact,
    Uri? backend,
  }) async {
    await root.create(recursive: true);

    final url = artifact.resolveAgainst(backend ?? Uri.parse('http://localhost'));
    final staging = Directory('${root.path}${Platform.pathSeparator}'
        '.staging-${offering.id}-${DateTime.now().microsecondsSinceEpoch}');
    await staging.create(recursive: true);

    final archive = File('${staging.path}${Platform.pathSeparator}artifact.tgz');
    try {
      await _download(url, archive, artifact.sha256);

      final content = Directory('${staging.path}${Platform.pathSeparator}content');
      await content.create(recursive: true);
      await extractTarGz(archive, content);

      final manifest = await _readManifest(content);
      manifest.validate();
      if (manifest.id != offering.id) {
        throw PluginFormatException(
            'archive holds "${manifest.id}", the registry named "${offering.id}"');
      }
      if (manifest.version != release.version) {
        throw PluginFormatException(
            'archive holds version "${manifest.version}", the registry named "${release.version}"');
      }
      if (!manifest.has('media.resolve') && !manifest.has('catalog.list')) {
        throw const PluginFormatException('the plugin declares nothing this client can use');
      }
      await _makeExecutable(content, manifest.entrypoint);

      final record = InstalledPluginRecord(
        id: manifest.id,
        version: manifest.version,
        protocol: manifest.protocol,
        name: manifest.name,
        description: manifest.description,
        entrypoint: manifest.entrypoint,
        capabilities: manifest.capabilities,
        sha256: artifact.sha256,
        os: artifact.os,
        arch: artifact.arch,
        enabled: true,
        directory: content,
      );
      await File('${content.path}${Platform.pathSeparator}installed.json')
          .writeAsString(jsonEncode(record.toJson()));

      // Move into place last, so a failure above leaves the previous install
      // exactly as it was.
      final target = _pluginDir(manifest.id);
      if (await target.exists()) {
        final old = Directory('${root.path}${Platform.pathSeparator}.old-${manifest.id}');
        if (await old.exists()) await old.delete(recursive: true);
        await target.rename(old.path);
        await content.rename(target.path);
        await old.delete(recursive: true);
      } else {
        await content.rename(target.path);
      }
      return (await byId(manifest.id))!;
    } finally {
      if (await staging.exists()) {
        await staging.delete(recursive: true);
      }
    }
  }

  Future<PluginManifest> _readManifest(Directory content) async {
    final file = File('${content.path}${Platform.pathSeparator}plugin.json');
    if (!await file.exists()) {
      throw const PluginFormatException('the archive has no plugin.json');
    }
    return PluginManifest.fromJson(jsonDecode(await file.readAsString()));
  }

  Future<void> _makeExecutable(Directory dir, String entrypoint) async {
    if (Platform.isWindows) return; // an .exe is executable by name
    final file = File('${dir.path}${Platform.pathSeparator}$entrypoint');
    try {
      await Process.run('chmod', ['0755', file.path]);
    } on ProcessException {
      // A filesystem without chmod (some Android tmpfs mounts) still runs the
      // file if it was extracted with its mode already set.
    }
  }

  /// Streams [url] into [target] while hashing it, so a 70 MB archive never
  /// sits in memory on a small device.
  Future<void> _download(Uri url, File target, String expectedSha) async {
    final request = http.Request('GET', url);
    final response = await _client.send(request).timeout(timeout);
    if (response.statusCode != 200) {
      throw PluginFormatException('download $url failed: HTTP ${response.statusCode}');
    }

    final sink = target.openWrite();
    final digest = _DigestSink();
    final hasher = sha256.startChunkedConversion(digest);
    var total = 0;
    try {
      await for (final chunk in response.stream.timeout(timeout)) {
        total += chunk.length;
        if (total > maxPluginArtifactBytes) {
          throw const PluginFormatException('archive is larger than this client accepts');
        }
        hasher.add(chunk);
        sink.add(chunk);
      }
    } finally {
      hasher.close();
      await sink.close();
    }

    final actual = digest.value.toString();
    if (actual != expectedSha.toLowerCase()) {
      await target.delete();
      throw PluginFormatException('archive hash mismatch: registry said $expectedSha, downloaded $actual');
    }
  }

  Future<void> remove(String id) async {
    final dir = _pluginDir(id);
    if (!await dir.exists()) return;
    await dir.delete(recursive: true);
  }

  Future<InstalledPluginRecord?> setEnabled(String id, bool enabled) async {
    final record = await byId(id);
    if (record == null) return null;
    final json = record.copyWith(enabled: enabled).toJson();
    await File('${record.directory.path}${Platform.pathSeparator}installed.json')
        .writeAsString(jsonEncode(json));
    return byId(id);
  }

  /// Which platform's artifacts this client asks for.
  Future<PluginPlatform> platform() => _platform();
}

class _DigestSink implements Sink<Digest> {
  late Digest value;

  @override
  void add(Digest data) => value = data;

  @override
  void close() {}
}

/// Extracts a gzipped tar archive into [dest].
///
/// Reads the archive as a stream, so the memory a large plugin needs stays
/// bounded by one tar block. Refuses anything that would leave [dest]: absolute
/// paths, `..`, symlinks and devices are skipped, and both the number of entries
/// and the expanded size are capped.
Future<void> extractTarGz(File archive, Directory dest) async {
  final input = archive.openRead().transform(gzip.decoder);
  final reader = _StreamReader(input);
  var written = 0;
  var entries = 0;

  while (true) {
    final header = await reader.read(512);
    if (header == null) break;
    if (header.every((b) => b == 0)) {
      // End-of-archive marker: one all-zero block, optionally followed by more.
      break;
    }
    if (++entries > maxPluginEntries) {
      throw const PluginFormatException('archive has too many entries');
    }

    final rawName = _cString(header, 0, 100);
    final prefix = _cString(header, 345, 155);
    final name = prefix.isEmpty ? rawName : '$prefix/$rawName';
    final size = _octal(header, 124, 12);
    final type = header[156];
    final mode = _octal(header, 100, 8);

    final padded = (size + 511) & ~511;

    if (!_safeEntryName(name)) {
      // Still has to be consumed, or the next header is read from the middle of
      // this entry's data.
      await reader.skip(padded);
      continue;
    }

    final isDir = type == 0x35; // '5'
    final isFile = type == 0x30 || type == 0; // '0' or old-style regular
    if (!isDir && !isFile) {
      await reader.skip(padded);
      continue;
    }

    final target = '${dest.path}${Platform.pathSeparator}${name.replaceAll('/', Platform.pathSeparator)}';
    if (isDir) {
      await Directory(target).create(recursive: true);
      await reader.skip(padded);
      continue;
    }

    written += size;
    if (written > maxPluginExtractedBytes) {
      throw const PluginFormatException('archive expands to more than this client accepts');
    }

    final file = File(target);
    await file.parent.create(recursive: true);
    final sink = file.openWrite();
    var remaining = size;
    while (remaining > 0) {
      final chunk = await reader.read(remaining < 64 * 1024 ? remaining : 64 * 1024);
      if (chunk == null) {
        await sink.close();
        throw const PluginFormatException('archive ended in the middle of an entry');
      }
      sink.add(chunk);
      remaining -= chunk.length;
    }
    await sink.close();
    if (mode > 0 && !Platform.isWindows) {
      await file.setLastModified(DateTime.now());
    }
    await reader.skip(padded - size);
  }
}

bool _safeEntryName(String name) {
  if (name.isEmpty || name.startsWith('/') || name.contains('\u0000')) return false;
  for (final part in name.split('/')) {
    if (part == '..') return false;
  }
  return true;
}

String _cString(Uint8List bytes, int offset, int length) {
  final end = offset + length;
  var i = offset;
  while (i < end && i < bytes.length && bytes[i] != 0) {
    i++;
  }
  return utf8.decode(bytes.sublist(offset, i), allowMalformed: true);
}

int _octal(Uint8List bytes, int offset, int length) {
  final text = _cString(bytes, offset, length).trim();
  if (text.isEmpty) return 0;
  return int.tryParse(text, radix: 8) ?? 0;
}

/// Reads exact-length chunks from a byte stream, with the ability to skip.
class _StreamReader {
  final StreamIterator<List<int>> _iterator;
  final List<int> _buffer = <int>[];
  int _offset = 0;

  _StreamReader(Stream<List<int>> stream) : _iterator = StreamIterator(stream);

  int get _available => _buffer.length - _offset;

  Future<bool> _fill(int need) async {
    while (_available < need) {
      if (!await _iterator.moveNext()) return false;
      if (_available == 0) {
        _buffer
          ..clear()
          ..addAll(_iterator.current);
        _offset = 0;
      } else {
        _buffer.addAll(_iterator.current);
      }
    }
    return true;
  }

  Future<Uint8List?> read(int length) async {
    if (length == 0) return Uint8List(0);
    if (!await _fill(length)) return null;
    final out = Uint8List.fromList(_buffer.sublist(_offset, _offset + length));
    _offset += length;
    _compact();
    return out;
  }

  Future<void> skip(int length) async {
    var remaining = length;
    while (remaining > 0) {
      final chunk = remaining < 64 * 1024 ? remaining : 64 * 1024;
      if (await read(chunk) == null) return;
      remaining -= chunk;
    }
  }

  void _compact() {
    if (_offset > 0 && _offset >= _buffer.length ~/ 2) {
      _buffer.removeRange(0, _offset);
      _offset = 0;
    }
  }
}
