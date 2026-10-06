// Test doubles for the updater: a fake release server (ReleaseSource) that
// serves real tar.gz packages built with the system `tar`, and helpers to lay
// out an installed root on disk.
import 'dart:io';

import 'package:client/update/install_layout.dart';
import 'package:client/update/release_source.dart';
import 'package:client/update/semver.dart';
import 'package:crypto/crypto.dart';

/// One release the fake server knows: the package bytes and the checksums text
/// (by default the honest ones).
class FakeReleaseFiles {
  final SemVer version;
  final List<int> package;
  String checksums;
  FakeReleaseFiles(this.version, this.package, this.checksums);
}

class FakeReleaseSource implements ReleaseSource {
  FakeReleaseFiles? latest;
  bool offline = false;
  bool failDownload = false;
  Duration downloadDelay = Duration.zero;
  String scheme = 'https';
  int latestCalls = 0;
  int downloads = 0;

  Uri _url(String name) => Uri.parse('$scheme://releases.example.test/$name');

  /// Publishes [files] as the newest release.
  void publish(FakeReleaseFiles files) => latest = files;

  @override
  Future<ReleaseInfo?> latestStable() async {
    latestCalls++;
    if (offline) throw const SocketException('network unreachable');
    final l = latest;
    if (l == null) return null;
    return ReleaseInfo(
      version: l.version,
      packageUrl: _url(clientPackageName(l.version)),
      checksumsUrl: _url(clientChecksumsName(l.version)),
    );
  }

  @override
  Future<String> fetchText(Uri url) async {
    if (offline) throw const SocketException('network unreachable');
    return latest!.checksums;
  }

  @override
  Future<void> download(Uri url, File dest) async {
    downloads++;
    if (offline || failDownload) throw const SocketException('connection reset');
    if (downloadDelay > Duration.zero) await Future<void>.delayed(downloadDelay);
    dest.writeAsBytesSync(latest!.package);
  }
}

/// Builds a release package the way the packaging script lays it out (one top
/// directory holding `bundle/client`), as a real tar.gz.
Future<FakeReleaseFiles> buildRelease(Directory scratch, String version, {bool withExecutable = true}) async {
  final v = SemVer.tryParse(version)!;
  final top = 'playheadtv-client-$v-$clientPlatform';
  final src = Directory('${scratch.path}/pkg-$version')..createSync(recursive: true);
  final bundle = Directory('${src.path}/$top/bundle')..createSync(recursive: true);
  if (withExecutable) {
    File('${bundle.path}/client').writeAsStringSync('#!/bin/sh\necho $version\n');
    Process.runSync('chmod', ['+x', '${bundle.path}/client']);
  } else {
    File('${bundle.path}/readme').writeAsStringSync('no executable here');
  }
  final archive = File('${scratch.path}/${clientPackageName(v)}');
  final result = Process.runSync('tar', ['-czf', archive.path, '-C', src.path, top]);
  if (result.exitCode != 0) throw StateError('tar failed: ${result.stderr}');
  final bytes = archive.readAsBytesSync();
  final digest = sha256.convert(bytes).toString();
  return FakeReleaseFiles(v, bytes, '$digest  ${clientPackageName(v)}\n');
}

/// Lays out an install root with [version] installed and current, like the
/// install script does.
InstallLayout installRoot(Directory root, String version) {
  final layout = InstallLayout(root);
  final v = SemVer.tryParse(version)!;
  final bundle = Directory('${layout.versionDir(v).path}/bundle')..createSync(recursive: true);
  File('${bundle.path}/client').writeAsStringSync('#!/bin/sh\necho $version\n');
  Process.runSync('chmod', ['+x', '${bundle.path}/client']);
  layout.pointAt(layout.currentLink, v);
  return layout;
}
