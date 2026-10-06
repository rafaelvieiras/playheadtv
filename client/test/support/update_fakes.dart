// Test doubles for the updater: a fake release server (ReleaseSource) that
// serves real tar.gz packages built with the system `tar`, a fake AppImage, an
// Ed25519 signer (TEST keys, generated here, never committed anywhere), and
// helpers to lay out an installed root on disk.
import 'dart:convert';
import 'dart:io';

import 'package:client/update/install_layout.dart';
import 'package:client/update/release_source.dart';
import 'package:client/update/semver.dart';
import 'package:client/update/signature.dart';
import 'package:crypto/crypto.dart';
import 'package:cryptography/cryptography.dart';

/// A throwaway Ed25519 key pair for tests.
class TestSigner {
  final SimpleKeyPair _pair;
  final String publicKey;
  TestSigner._(this._pair, this.publicKey);

  static Future<TestSigner> generate() async {
    final pair = await Ed25519().newKeyPair();
    final pub = await pair.extractPublicKey();
    return TestSigner._(pair, base64.encode(pub.bytes));
  }

  /// Base64 text of the detached signature, the format of the `.sig` asset.
  Future<String> sign(List<int> message) async {
    final signature = await Ed25519().sign(message, keyPair: _pair);
    return base64.encode(signature.bytes);
  }

  UpdateVerifier get verifier => UpdateVerifier(publicKeys: [publicKey]);
}

/// One release the fake server knows: the package bytes, the checksums text
/// and its signature (by default the honest ones).
class FakeReleaseFiles {
  final SemVer version;
  final List<int> package;
  List<int> checksums;
  String? signature;
  List<int>? appImage;
  String notes;
  FakeReleaseFiles(this.version, this.package, this.checksums, {this.signature, this.appImage, this.notes = ''});

  String get checksumsText => utf8.decode(checksums);
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
      signatureUrl: l.signature == null ? null : _url(clientSignatureName(l.version)),
      appImageUrl: l.appImage == null ? null : _url(clientAppImageName(l.version)),
      notes: l.notes,
    );
  }

  @override
  Future<List<int>> fetchBytes(Uri url) async {
    if (offline) throw const SocketException('network unreachable');
    if (url.path.endsWith('.sig')) return utf8.encode(latest!.signature!);
    return latest!.checksums;
  }

  @override
  Future<void> download(Uri url, File dest) async {
    downloads++;
    if (offline || failDownload) throw const SocketException('connection reset');
    if (downloadDelay > Duration.zero) await Future<void>.delayed(downloadDelay);
    dest.writeAsBytesSync(url.path.endsWith('.AppImage') ? latest!.appImage! : latest!.package);
  }
}

/// A fake AppImage: starts with the ELF magic, like the real runtime.
List<int> fakeAppImageBytes(String version) => [0x7f, 0x45, 0x4c, 0x46, ...utf8.encode('fake appimage $version')];

/// Builds a release the way the packaging scripts lay it out: a real tar.gz
/// (one top directory holding `bundle/client`), a fake AppImage when
/// [withAppImage], and a checksums file listing them, signed by [signer] (no
/// signature at all when null).
Future<FakeReleaseFiles> buildRelease(
  Directory scratch,
  String version, {
  bool withExecutable = true,
  TestSigner? signer,
  bool withAppImage = false,
  String notes = '',
}) async {
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
  final lines = StringBuffer('${sha256.convert(bytes)}  ${clientPackageName(v)}\n');
  final appImage = withAppImage ? fakeAppImageBytes(version) : null;
  if (appImage != null) lines.write('${sha256.convert(appImage)}  ${clientAppImageName(v)}\n');
  final checksums = utf8.encode(lines.toString());
  return FakeReleaseFiles(
    v,
    bytes,
    checksums,
    signature: signer == null ? null : await signer.sign(checksums),
    appImage: appImage,
    notes: notes,
  );
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
