// The AppImage flavour of the updater: the same flow as the tar.gz install, but
// what is swapped is the AppImage file itself (fake ELF-prefixed files here, a
// fake release server, a real Ed25519 test signature).
import 'dart:convert';
import 'dart:io';

import 'package:client/update/install_layout.dart';
import 'package:client/update/install_target.dart';
import 'package:client/update/release_source.dart';
import 'package:client/update/semver.dart';
import 'package:client/update/updater.dart';
import 'package:crypto/crypto.dart';
import 'package:flutter_test/flutter_test.dart';

import 'support/update_fakes.dart';

void main() {
  late Directory scratch;
  late File appImage;
  late File stateFile;
  late FakeReleaseSource server;
  late DateTime clock;
  late TestSigner signer;
  final logs = <String>[];

  setUpAll(() async => signer = await TestSigner.generate());

  setUp(() {
    scratch = Directory.systemTemp.createTempSync('playheadtv_appimage_');
    final apps = Directory('${scratch.path}/Applications')..createSync();
    appImage = File('${apps.path}/Playhead-TV.AppImage')..writeAsBytesSync(fakeAppImageBytes('1.0.0'));
    Process.runSync('chmod', ['755', appImage.path]);
    stateFile = File('${scratch.path}/state/playheadtv/state.json'); // the directory does not exist yet
    server = FakeReleaseSource();
    clock = DateTime.utc(2030, 1, 1, 12);
    logs.clear();
  });

  tearDown(() {
    Process.runSync('chmod', ['-R', '755', scratch.path]);
    scratch.deleteSync(recursive: true);
  });

  Updater updater({String running = '1.0.0', UpdateSettings settings = const UpdateSettings()}) => Updater(
        target: AppImageTarget(appImage: appImage, running: SemVer.tryParse(running)!, stateFile: stateFile),
        source: server,
        verifier: signer.verifier,
        settings: settings,
        now: () => clock,
        healthyWindow: const Duration(seconds: 15),
        log: logs.add,
      );

  Future<void> publish(String version, {bool withAppImage = true, bool signed = true, String notes = ''}) async =>
      server.publish(await buildRelease(scratch, version,
          signer: signed ? signer : null, withAppImage: withAppImage, notes: notes));

  List<int> installedBytes() => appImage.readAsBytesSync();
  File previous() => File('${appImage.path}.previous');
  Directory staging() => Directory('${appImage.path}.update');

  group('AppImage update', () {
    test('the file is replaced, the previous one kept, a pending marker and the changelog recorded', () async {
      await publish('1.1.0', notes: 'Fixed things.\r\n\r\n\r\n\r\nAdded stuff.');
      final result = await updater().prepare();

      expect(result.outcome, UpdateOutcome.updated);
      expect(installedBytes(), fakeAppImageBytes('1.1.0'));
      expect(previous().readAsBytesSync(), fakeAppImageBytes('1.0.0'), reason: 'previous kept for rollback');
      expect((appImage.statSync().mode & 0x49) != 0, isTrue, reason: 'still executable');
      expect(staging().existsSync(), isFalse);
      final state = UpdateState.read(stateFile);
      expect((state.pending!.version, state.pending!.previous, state.pending!.confirmed), ('1.1.0', '1.0.0', false));
      expect(state.lastUpdate!.version, '1.1.0');
      expect(state.lastUpdate!.previous, '1.0.0');
      expect(state.lastUpdate!.notes, 'Fixed things.\n\nAdded stuff.', reason: 'stored sanitised, as plain text');
    });

    test('a process that already runs the old file keeps working: the old inode survives the swap', () async {
      final running = appImage.openSync();
      await publish('1.1.0');
      await updater().prepare();
      expect(running.readSync(4), [0x7f, 0x45, 0x4c, 0x46]);
      expect(String.fromCharCodes(running.readSync(20)), contains('1.0.0'));
      running.closeSync();
    });

    test('an update not confirmed in time is rolled back: the previous file is restored', () async {
      await publish('1.1.0');
      await updater().prepare();

      clock = clock.add(const Duration(seconds: 16)); // the new AppImage never confirmed
      server.offline = true;
      final result = await updater(running: '1.1.0').prepare(); // started from the new file

      expect(result.rolledBackFrom, '1.1.0');
      expect(installedBytes(), fakeAppImageBytes('1.0.0'));
      final state = UpdateState.read(stateFile);
      expect(state.pending, isNull);
      expect(state.failed, {'1.1.0'});
      expect(state.lastUpdate, isNull, reason: 'no "updated to 1.1.0" notice for a version that was rolled back');
    });

    test('within the confirmation window nothing happens', () async {
      await publish('1.1.0');
      await updater().prepare();
      clock = clock.add(const Duration(seconds: 5));
      final result = await updater(running: '1.1.0').prepare();
      expect(result.outcome, UpdateOutcome.pendingStart);
      expect(installedBytes(), fakeAppImageBytes('1.1.0'));
    });

    test('a confirmed update is kept and the marker cleared, the rollback copy stays', () async {
      await publish('1.1.0');
      await updater().prepare();
      confirmPendingHealthyAt(stateFile, SemVer.tryParse('1.1.0')!);
      clock = clock.add(const Duration(minutes: 5));
      final result = await updater(running: '1.1.0').prepare();
      expect(result.rolledBackFrom, isNull);
      expect(installedBytes(), fakeAppImageBytes('1.1.0'));
      final state = UpdateState.read(stateFile);
      expect(state.pending, isNull);
      expect(state.lastUpdate!.version, '1.1.0', reason: 'kept so the app can show the changelog once');
      expect(previous().existsSync(), isTrue);
    });

    test('a rolled back version is not offered again, a newer one is', () async {
      await publish('1.1.0');
      await updater().prepare();
      clock = clock.add(const Duration(minutes: 1));
      final after = await updater(running: '1.1.0').prepare();
      expect(after.rolledBackFrom, '1.1.0');
      expect(after.outcome, UpdateOutcome.upToDate);

      await publish('1.1.1');
      expect((await updater().prepare()).outcome, UpdateOutcome.updated);
      expect(installedBytes(), fakeAppImageBytes('1.1.1'));
    });

    test('a release without an AppImage asset leaves the file untouched', () async {
      await publish('1.1.0', withAppImage: false);
      final result = await updater().prepare();
      expect(result.outcome, UpdateOutcome.checkFailed);
      expect(result.message, contains('AppImage'));
      expect(installedBytes(), fakeAppImageBytes('1.0.0'));
      expect(previous().existsSync(), isFalse);
    });

    test('an unsigned or wrongly signed release is refused before any download', () async {
      await publish('1.1.0', signed: false);
      var result = await updater().prepare();
      expect(result.outcome, UpdateOutcome.signatureRejected);

      final other = await TestSigner.generate();
      server.publish(await buildRelease(scratch, '1.1.0', signer: other, withAppImage: true));
      result = await updater().prepare();
      expect(result.outcome, UpdateOutcome.signatureRejected);
      expect(server.downloads, 0);
      expect(installedBytes(), fakeAppImageBytes('1.0.0'));
    });

    test('an AppImage whose hash is not the signed one is refused', () async {
      await publish('1.1.0');
      server.latest!.appImage = fakeAppImageBytes('6.6.6'); // swapped after signing
      final result = await updater().prepare();
      expect(result.outcome, UpdateOutcome.checksumMismatch);
      expect(installedBytes(), fakeAppImageBytes('1.0.0'));
      expect(previous().existsSync(), isFalse);
      expect(staging().existsSync(), isFalse);
    });

    test('a signed file that is not an ELF executable is refused', () async {
      final release = await buildRelease(scratch, '1.1.0', signer: signer, withAppImage: true);
      release.appImage = utf8.encode('<html>not an appimage</html>');
      release.checksums = utf8.encode(
          '${sha256.convert(release.package)}  ${clientPackageName(release.version)}\n${sha256.convert(release.appImage!)}  ${clientAppImageName(release.version)}\n');
      release.signature = await signer.sign(release.checksums);
      server.publish(release);
      final result = await updater().prepare();
      expect(result.outcome, UpdateOutcome.failed);
      expect(installedBytes(), fakeAppImageBytes('1.0.0'));
      expect(previous().existsSync(), isFalse);
    });

    test('a folder that cannot be written fails early, before downloading', () async {
      if (Process.runSync('id', ['-u']).stdout.toString().trim() == '0') return; // root ignores permissions
      await publish('1.1.0');
      Process.runSync('chmod', ['555', appImage.parent.path]);
      final result = await updater().prepare();
      Process.runSync('chmod', ['755', appImage.parent.path]);
      expect(result.outcome, UpdateOutcome.failed);
      expect(server.downloads, 0);
      expect(installedBytes(), fakeAppImageBytes('1.0.0'));
    });

    test('auto-update off still rolls back, and never checks', () async {
      await publish('1.1.0');
      await updater().prepare();
      clock = clock.add(const Duration(minutes: 1));
      final result = await updater(running: '1.1.0', settings: const UpdateSettings(autoUpdate: false)).prepare();
      expect(result.rolledBackFrom, '1.1.0');
      expect(installedBytes(), fakeAppImageBytes('1.0.0'));
    });

    test('a missing AppImage file means nothing to update', () async {
      appImage.deleteSync();
      await publish('1.1.0');
      expect((await updater().prepare()).outcome, UpdateOutcome.notInstalled);
    });

    test('the state directory follows XDG_STATE_HOME, then HOME', () {
      expect(appImageStateDir({'XDG_STATE_HOME': '/x', 'HOME': '/h'})!.path, '/x/playheadtv');
      expect(appImageStateDir({'HOME': '/h'})!.path, '/h/.local/state/playheadtv');
      expect(appImageStateDir({}), isNull);
    });
  });
}
