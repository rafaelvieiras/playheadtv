import 'dart:convert';
import 'dart:io';

import 'package:client/update/install_layout.dart';
import 'package:client/update/install_target.dart';
import 'package:client/update/release_source.dart';
import 'package:client/update/semver.dart';
import 'package:client/update/signature.dart';
import 'package:client/update/updater.dart';
import 'package:flutter_test/flutter_test.dart';

import 'support/update_fakes.dart';

void main() {
  late Directory scratch;
  late InstallLayout layout;
  late FakeReleaseSource server;
  late DateTime clock;
  final logs = <String>[];
  late TestSigner signer;

  setUpAll(() async => signer = await TestSigner.generate());

  setUp(() {
    scratch = Directory.systemTemp.createTempSync('playheadtv_updater_');
    layout = installRoot(Directory('${scratch.path}/root')..createSync(), '1.0.0');
    server = FakeReleaseSource();
    clock = DateTime.utc(2030, 1, 1, 12);
    logs.clear();
  });

  tearDown(() => scratch.deleteSync(recursive: true));

  Updater updaterWith({UpdateSettings settings = const UpdateSettings(), Duration budget = const Duration(seconds: 30)}) =>
      Updater(
        target: TarballTarget(layout),
        source: server,
        verifier: signer.verifier,
        settings: settings,
        now: () => clock,
        healthyWindow: const Duration(seconds: 15),
        budget: budget,
        log: logs.add,
      );

  Future<void> publish(String version) async => server.publish(await buildRelease(scratch, version, signer: signer));

  void expectUntouched() {
    expect(layout.installedVersion, SemVer.tryParse('1.0.0'));
    expect(layout.stagingDir.existsSync(), isFalse, reason: 'staging is cleaned up');
    expect(UpdateState.read(layout.stateFile).pending, isNull);
  }

  group('update flow', () {
    test('a newer release is downloaded, verified, swapped in, and marked pending', () async {
      await publish('1.1.0');
      final result = await updaterWith().prepare();

      expect(result.outcome, UpdateOutcome.updated);
      expect(result.version, SemVer.tryParse('1.1.0'));
      expect(layout.installedVersion, SemVer.tryParse('1.1.0'));
      expect(layout.previousVersion, SemVer.tryParse('1.0.0'));
      expect(layout.executableOf(SemVer.tryParse('1.1.0')!).existsSync(), isTrue);
      expect(layout.executableOf(SemVer.tryParse('1.0.0')!).existsSync(), isTrue, reason: 'previous kept for rollback');
      expect(layout.stagingDir.existsSync(), isFalse);
      final pending = UpdateState.read(layout.stateFile).pending!;
      expect((pending.version, pending.previous, pending.confirmed), ('1.1.0', '1.0.0', false));
    });

    test('the same version is not downloaded again', () async {
      await publish('1.0.0');
      final result = await updaterWith().prepare();
      expect(result.outcome, UpdateOutcome.upToDate);
      expect(server.downloads, 0);
      expectUntouched();
    });

    test('an older release is ignored', () async {
      await publish('0.9.0');
      expect((await updaterWith().prepare()).outcome, UpdateOutcome.upToDate);
      expectUntouched();
    });

    test('no release published is up to date', () async {
      expect((await updaterWith().prepare()).outcome, UpdateOutcome.upToDate);
      expectUntouched();
    });

    test('offline: the check fails and the installed version is untouched', () async {
      await publish('1.1.0');
      server.offline = true;
      final result = await updaterWith().prepare();
      expect(result.outcome, UpdateOutcome.checkFailed);
      expectUntouched();
    });

    test('a failed download leaves the installed version in place', () async {
      await publish('1.1.0');
      server.failDownload = true;
      final result = await updaterWith().prepare();
      expect(result.outcome, UpdateOutcome.downloadFailed);
      expectUntouched();
      expect(layout.versionDir(SemVer.tryParse('1.1.0')!).existsSync(), isFalse);
    });

    test('a checksum mismatch is rejected and nothing is installed', () async {
      // A correctly SIGNED list that does not match the package: integrity is
      // still checked separately from authenticity.
      final release = await buildRelease(scratch, '1.1.0', signer: signer);
      release.checksums = utf8.encode('${'0' * 64}  ${clientPackageName(release.version)}\n');
      release.signature = await signer.sign(release.checksums);
      server.publish(release);
      final result = await updaterWith().prepare();
      expect(result.outcome, UpdateOutcome.checksumMismatch);
      expectUntouched();
      expect(layout.versionDir(release.version).existsSync(), isFalse);
    });

    test('a package missing from the checksums file is rejected', () async {
      final release = await buildRelease(scratch, '1.1.0', signer: signer);
      release.checksums = utf8.encode('${'a' * 64}  some-other-file.tar.gz\n');
      release.signature = await signer.sign(release.checksums);
      server.publish(release);
      expect((await updaterWith().prepare()).outcome, UpdateOutcome.checksumMismatch);
      expectUntouched();
    });

    test('assets not served over HTTPS are refused', () async {
      await publish('1.1.0');
      server.scheme = 'http';
      final result = await updaterWith().prepare();
      expect(result.outcome, UpdateOutcome.checkFailed);
      expect(server.downloads, 0);
      expectUntouched();
    });

    test('a package without bundle/client is rejected', () async {
      server.publish(await buildRelease(scratch, '1.1.0', withExecutable: false, signer: signer));
      final result = await updaterWith().prepare();
      expect(result.outcome, UpdateOutcome.failed);
      expectUntouched();
    });

    test('an update past its time budget is dropped', () async {
      await publish('1.1.0');
      server.downloadDelay = const Duration(seconds: 2);
      final result = await updaterWith(budget: const Duration(milliseconds: 100)).prepare();
      expect(result.outcome, UpdateOutcome.failed);
      expect(result.message, contains('budget'));
      expect(layout.installedVersion, SemVer.tryParse('1.0.0'));
    });

    test('the disable switch skips the check entirely', () async {
      await publish('1.1.0');
      final result = await updaterWith(settings: const UpdateSettings(autoUpdate: false)).prepare();
      expect(result.outcome, UpdateOutcome.disabled);
      expect(server.latestCalls, 0);
      expectUntouched();
    });

    test('without an installation there is nothing to update', () async {
      layout.currentLink.deleteSync();
      await publish('1.1.0');
      expect((await updaterWith().prepare()).outcome, UpdateOutcome.notInstalled);
      expect(server.downloads, 0);
    });
  });

  group('signed releases', () {
    Future<void> expectRefused(UpdateResult result, String reasonFragment) async {
      expect(result.outcome, UpdateOutcome.signatureRejected);
      expect(result.message, contains(reasonFragment));
      expect(server.downloads, 0, reason: 'the package is not even downloaded');
      expect(logs.any((l) => l.contains('refusing release')), isTrue, reason: 'the refusal is logged');
      expectUntouched();
    }

    test('a release signed with the pinned key is accepted', () async {
      await publish('1.1.0');
      final result = await updaterWith().prepare();
      expect(result.outcome, UpdateOutcome.updated);
      expect(server.downloads, 1);
    });

    test('a signature made with another key is refused (key mismatch)', () async {
      final other = await TestSigner.generate();
      server.publish(await buildRelease(scratch, '1.1.0', signer: other));
      await expectRefused(await updaterWith().prepare(), 'does not verify');
    });

    test('a missing signature is a refusal, not a warning', () async {
      server.publish(await buildRelease(scratch, '1.1.0'));
      await expectRefused(await updaterWith().prepare(), 'no signature');
    });

    test('a garbled signature is refused', () async {
      final release = await buildRelease(scratch, '1.1.0', signer: signer);
      release.signature = 'not base64 !!';
      server.publish(release);
      await expectRefused(await updaterWith().prepare(), 'base64');
    });

    test('a signature of the wrong length is refused', () async {
      final release = await buildRelease(scratch, '1.1.0', signer: signer);
      release.signature = base64.encode(List.filled(10, 1));
      server.publish(release);
      await expectRefused(await updaterWith().prepare(), 'expected 64');
    });

    test('a tampered checksums file no longer matches its signature', () async {
      final release = await buildRelease(scratch, '1.1.0', signer: signer);
      // An attacker swaps the package and rewrites the list to match it, but
      // cannot re-sign it.
      release.checksums = utf8.encode('${'f' * 64}  ${clientPackageName(release.version)}\n');
      server.publish(release);
      await expectRefused(await updaterWith().prepare(), 'does not verify');
    });

    test('a build that pins no key refuses every update', () async {
      await publish('1.1.0');
      final updater = Updater(
        target: TarballTarget(layout),
        source: server,
        verifier: const UpdateVerifier(publicKeys: []),
        now: () => clock,
        log: logs.add,
      );
      final result = await updater.prepare();
      expect(result.outcome, UpdateOutcome.signatureRejected);
      expect(result.message, contains('no update signing key'));
      expect(server.downloads, 0);
      expectUntouched();
    });

    test('the explicit development switch accepts an unsigned release only when no key is pinned', () async {
      server.publish(await buildRelease(scratch, '1.1.0'));
      final dev = Updater(
        target: TarballTarget(layout),
        source: server,
        verifier: const UpdateVerifier(publicKeys: [], allowUnsigned: true),
        now: () => clock,
        log: logs.add,
      );
      expect((await dev.prepare()).outcome, UpdateOutcome.updated);
      expect(logs.any((l) => l.contains('NOT authenticated')), isTrue, reason: 'it says so loudly');
    });

    test('the development switch does not weaken a build that pins a key', () async {
      server.publish(await buildRelease(scratch, '1.1.0'));
      final updater = Updater(
        target: TarballTarget(layout),
        source: server,
        verifier: UpdateVerifier(publicKeys: [signer.publicKey], allowUnsigned: true),
        now: () => clock,
        log: logs.add,
      );
      expect((await updater.prepare()).outcome, UpdateOutcome.signatureRejected);
      expectUntouched();
    });

    test('a rotated key list accepts a release signed by either key', () async {
      final oldSigner = signer;
      final newSigner = await TestSigner.generate();
      final rotated = UpdateVerifier(publicKeys: [oldSigner.publicKey, newSigner.publicKey]);
      Updater with_(UpdateVerifier v) => Updater(target: TarballTarget(layout), source: server, verifier: v, now: () => clock, log: logs.add);

      server.publish(await buildRelease(scratch, '1.1.0', signer: newSigner));
      expect((await with_(rotated).prepare()).outcome, UpdateOutcome.updated, reason: 'signed with the new key');

      // The old key retired: a release signed with it is no longer accepted.
      final retired = UpdateVerifier(publicKeys: [newSigner.publicKey]);
      server.publish(await buildRelease(scratch, '1.2.0', signer: oldSigner));
      confirmPendingHealthy(layout, SemVer.tryParse('1.1.0')!);
      clock = clock.add(const Duration(minutes: 1));
      expect((await with_(retired).prepare()).outcome, UpdateOutcome.signatureRejected);
      expect(layout.installedVersion, SemVer.tryParse('1.1.0'));
      // ... while a clients still holding both keys accepts it.
      expect((await with_(rotated).prepare()).outcome, UpdateOutcome.updated);
    });

    test('a malformed pinned key never verifies anything', () async {
      await publish('1.1.0');
      final updater = Updater(
        target: TarballTarget(layout),
        source: server,
        verifier: const UpdateVerifier(publicKeys: ['not a key', 'AAAA']),
        now: () => clock,
        log: logs.add,
      );
      expect((await updater.prepare()).outcome, UpdateOutcome.signatureRejected);
    });
  });

  group('preflight (the local half the launcher runs before the app)', () {
    test('rolls back an unconfirmed update without touching the release server', () async {
      await publish('1.1.0');
      await updaterWith().prepare(); // swaps in 1.1.0, pending
      clock = clock.add(const Duration(seconds: 16)); // the app never confirmed
      server.offline = true; // any network use would be refused
      server.latestCalls = 0; // counted from here: the prepare above used it

      final result = await updaterWith().preflight();

      expect(result.rolledBackFrom, '1.1.0');
      expect(result.inWindow, isFalse);
      expect(layout.installedVersion, SemVer.tryParse('1.0.0'));
      expect(UpdateState.read(layout.stateFile).failed, {'1.1.0'});
      expect(server.latestCalls, 0, reason: 'preflight never checks for a new release');
    });

    test('reports the confirmation window so the background half stays out of it', () async {
      await publish('1.1.0');
      await updaterWith().prepare();
      clock = clock.add(const Duration(seconds: 5));

      final result = await updaterWith().preflight();

      expect(result.inWindow, isTrue);
      expect(result.rolledBackFrom, isNull);
      expect(layout.installedVersion, SemVer.tryParse('1.1.0'));
    });

    test('does nothing when there is nothing pending', () async {
      server.offline = true;
      final result = await updaterWith().preflight();
      expect(result.inWindow, isFalse);
      expect(result.rolledBackFrom, isNull);
      expect(layout.installedVersion, SemVer.tryParse('1.0.0'));
    });

    test('preflight then prepare still installs a newer release', () async {
      await publish('1.1.0');
      await updaterWith().preflight();
      final result = await updaterWith().prepare();
      expect(result.outcome, UpdateOutcome.updated);
      expect(layout.installedVersion, SemVer.tryParse('1.1.0'));
    });
  });

  group('crash rollback', () {
    test('an update not confirmed within the window is rolled back on the next start', () async {
      await publish('1.1.0');
      await updaterWith().prepare(); // swaps in 1.1.0, pending

      clock = clock.add(const Duration(seconds: 16)); // the app never confirmed
      server.offline = true; // isolate the rollback from any new update
      final result = await updaterWith().prepare();

      expect(result.rolledBackFrom, '1.1.0');
      expect(layout.installedVersion, SemVer.tryParse('1.0.0'));
      final state = UpdateState.read(layout.stateFile);
      expect(state.pending, isNull);
      expect(state.failed, {'1.1.0'});
      expect(layout.versionDir(SemVer.tryParse('1.1.0')!).existsSync(), isFalse, reason: 'bad version removed');
    });

    test('a rolled back version is not offered again, a newer one is', () async {
      await publish('1.1.0');
      await updaterWith().prepare();
      clock = clock.add(const Duration(minutes: 1));
      final afterRollback = await updaterWith().prepare(); // 1.1.0 is still the latest
      expect(afterRollback.rolledBackFrom, '1.1.0');
      expect(afterRollback.outcome, UpdateOutcome.upToDate);
      expect(layout.installedVersion, SemVer.tryParse('1.0.0'));

      await publish('1.1.1');
      final fixed = await updaterWith().prepare();
      expect(fixed.outcome, UpdateOutcome.updated);
      expect(layout.installedVersion, SemVer.tryParse('1.1.1'));
    });

    test('within the window nothing happens yet', () async {
      await publish('1.1.0');
      await updaterWith().prepare();
      clock = clock.add(const Duration(seconds: 5));
      final result = await updaterWith().prepare();
      expect(result.outcome, UpdateOutcome.pendingStart);
      expect(layout.installedVersion, SemVer.tryParse('1.1.0'));
    });

    test('a confirmed update is kept, the marker cleared and old versions pruned', () async {
      await publish('1.1.0');
      await updaterWith().prepare();
      confirmPendingHealthy(layout, SemVer.tryParse('1.1.0')!);

      clock = clock.add(const Duration(minutes: 5));
      final result = await updaterWith().prepare();
      expect(result.rolledBackFrom, isNull);
      expect(layout.installedVersion, SemVer.tryParse('1.1.0'));
      expect(UpdateState.read(layout.stateFile).pending, isNull);
      expect(UpdateState.read(layout.stateFile).failed, isEmpty);
    });

    test('confirming another version does not confirm the pending one', () async {
      await publish('1.1.0');
      await updaterWith().prepare();
      confirmPendingHealthy(layout, SemVer.tryParse('1.0.0')!);
      expect(UpdateState.read(layout.stateFile).pending!.confirmed, isFalse);
    });

    test('the disable switch still rolls back a crashed update', () async {
      await publish('1.1.0');
      await updaterWith().prepare();
      clock = clock.add(const Duration(minutes: 1));
      final result = await updaterWith(settings: const UpdateSettings(autoUpdate: false)).prepare();
      expect(result.rolledBackFrom, '1.1.0');
      expect(layout.installedVersion, SemVer.tryParse('1.0.0'));
    });

    test('a missing previous version cannot be rolled back to, without crashing', () async {
      await publish('1.1.0');
      await updaterWith().prepare();
      layout.versionDir(SemVer.tryParse('1.0.0')!).deleteSync(recursive: true);
      clock = clock.add(const Duration(minutes: 1));
      server.offline = true;
      final result = await updaterWith().prepare();
      expect(result.rolledBackFrom, '1.1.0');
      expect(layout.installedVersion, SemVer.tryParse('1.1.0'), reason: 'nothing to go back to');
      expect(UpdateState.read(layout.stateFile).pending, isNull);
    });

    test('a stale marker (swap never happened) is dropped, not treated as a crash', () async {
      UpdateState(pending: PendingUpdate(version: '2.0.0', previous: '1.0.0', startedAt: clock.subtract(const Duration(hours: 1))))
          .write(layout.stateFile);
      server.offline = true;
      final result = await updaterWith().prepare();
      expect(result.rolledBackFrom, isNull);
      expect(layout.installedVersion, SemVer.tryParse('1.0.0'));
      expect(UpdateState.read(layout.stateFile).pending, isNull);
    });

    test('a corrupt state file is an empty state', () async {
      layout.stateFile.writeAsStringSync('{not json');
      await publish('1.1.0');
      expect((await updaterWith().prepare()).outcome, UpdateOutcome.updated);
    });
  });

  group('helpers', () {
    test('expectedChecksum reads sha256sum output', () {
      final h = 'ab' * 32;
      expect(expectedChecksum('$h  a.tar.gz\n${'cd' * 32} *b.tar.gz\n', 'a.tar.gz'), h);
      expect(expectedChecksum('$h  a.tar.gz\n${'CD' * 32} *b.tar.gz\n', 'b.tar.gz'), 'cd' * 32);
      expect(expectedChecksum('$h  a.tar.gz\n', 'c.tar.gz'), isNull);
      expect(expectedChecksum('garbage', 'a.tar.gz'), isNull);
    });

    test('settings come from the config, the environment switch wins', () {
      expect(UpdateSettings.fromConfig({}).autoUpdate, isTrue);
      expect(UpdateSettings.fromConfig({}).channel, 'stable');
      expect(UpdateSettings.fromConfig({'auto_update': false}).autoUpdate, isFalse);
      expect(UpdateSettings.fromConfig({'auto_update': 'no'}).autoUpdate, isTrue, reason: 'wrong type keeps the default');
      expect(UpdateSettings.fromConfig({'auto_update': true}, env: {'PLAYHEADTV_AUTO_UPDATE': '0'}).autoUpdate, isFalse);
      expect(UpdateSettings.fromConfig({'update_channel': 'stable'}).channel, 'stable');
    });

    test('an unsupported channel falls back to stable', () async {
      await publish('1.1.0');
      final result = await updaterWith(settings: const UpdateSettings(channel: 'nightly')).prepare();
      expect(result.outcome, UpdateOutcome.updated);
      expect(logs.any((l) => l.contains('nightly')), isTrue);
    });
  });
}
