import 'dart:io';

import 'package:client/update/install_layout.dart';
import 'package:client/update/release_source.dart';
import 'package:client/update/semver.dart';
import 'package:client/update/updater.dart';
import 'package:flutter_test/flutter_test.dart';

import 'support/update_fakes.dart';

void main() {
  late Directory scratch;
  late InstallLayout layout;
  late FakeReleaseSource server;
  late DateTime clock;
  final logs = <String>[];

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
        layout: layout,
        source: server,
        settings: settings,
        now: () => clock,
        healthyWindow: const Duration(seconds: 15),
        budget: budget,
        log: logs.add,
      );

  Future<void> publish(String version) async => server.publish(await buildRelease(scratch, version));

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
      final release = await buildRelease(scratch, '1.1.0');
      release.checksums = '${'0' * 64}  ${clientPackageName(release.version)}\n';
      server.publish(release);
      final result = await updaterWith().prepare();
      expect(result.outcome, UpdateOutcome.checksumMismatch);
      expectUntouched();
      expect(layout.versionDir(release.version).existsSync(), isFalse);
    });

    test('a package missing from the checksums file is rejected', () async {
      final release = await buildRelease(scratch, '1.1.0');
      release.checksums = '${'a' * 64}  some-other-file.tar.gz\n';
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
      server.publish(await buildRelease(scratch, '1.1.0', withExecutable: false));
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
