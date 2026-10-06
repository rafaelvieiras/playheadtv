// Install kind detection, the banner wording per kind, and the controller that
// decides when the banner and the post-update changelog are shown.
import 'dart:io';

import 'package:client/update/app_update.dart';
import 'package:client/update/install_kind.dart';
import 'package:client/update/install_layout.dart';
import 'package:client/update/semver.dart';
import 'package:client/update/update_banner.dart';
import 'package:client/update/update_notices.dart';
import 'package:flutter_test/flutter_test.dart';

import 'support/update_fakes.dart';

SemVer v(String s) => SemVer.tryParse(s)!;

class MemoryStore implements UpdateNoticeStore {
  @override
  SemVer? dismissedVersion;
  @override
  String? changelogShownFor;
  @override
  Future<void> setDismissedVersion(SemVer version) async => dismissedVersion = version;
  @override
  Future<void> setChangelogShownFor(String version) async => changelogShownFor = version;
}

void main() {
  group('install kind detection', () {
    InstallKind kind(Map<String, String> env, String exe) => detectInstallKind(env: env, executablePath: exe);

    test('a Flatpak or Snap sandbox, even with other markers present', () {
      expect(kind({'FLATPAK_ID': 'io.example.App'}, '/app/bin/client'), InstallKind.sandbox);
      expect(kind({'SNAP': '/snap/playheadtv/1'}, '/snap/playheadtv/1/bin/client'), InstallKind.sandbox);
      expect(kind({'FLATPAK_ID': 'x', 'APPIMAGE': '/a.AppImage'}, '/tmp/.mount_x/bundle/client'), InstallKind.sandbox);
    });

    test('an AppImage (APPIMAGE exported by the runtime)', () {
      expect(kind({'APPIMAGE': '/home/u/Apps/Playhead.AppImage'}, '/tmp/.mount_PlayhXYZ/bundle/client'), InstallKind.appImage);
    });

    test('the tar.gz user install, from the launcher root and the binary path', () {
      const root = '/home/u/.local/share/playheadtv';
      expect(kind({'PLAYHEADTV_ROOT': root}, '$root/versions/1.2.3/bundle/client'), InstallKind.tarball);
      expect(kind({'PLAYHEADTV_ROOT': '$root/'}, '$root/versions/1.2.3/bundle/client'), InstallKind.tarball);
    });

    test('a launcher root that does not contain the binary is not the tar.gz install', () {
      expect(kind({'PLAYHEADTV_ROOT': '/home/u/.local/share/playheadtv'}, '/home/u/src/client/build/linux/x64/release/bundle/client'),
          InstallKind.unknown);
      // A sibling directory with the same prefix is not inside the root.
      expect(kind({'PLAYHEADTV_ROOT': '/home/u/pl'}, '/home/u/playheadtv/bundle/client'), InstallKind.unknown);
    });

    test('a system package or another system-managed path', () {
      expect(kind({}, '/usr/lib/playheadtv/client'), InstallKind.systemPackage);
      expect(kind({}, '/usr/bin/client'), InstallKind.systemPackage);
      expect(kind({}, '/opt/playheadtv/bundle/client'), InstallKind.systemPackage);
    });

    test('a source build, a manual copy or anything else is unknown', () {
      expect(kind({}, '/home/u/src/playheadtv/client/build/linux/x64/release/bundle/client'), InstallKind.unknown);
      expect(kind({'HOME': '/home/u'}, '/home/u/bin/client'), InstallKind.unknown);
      expect(kind({'FLATPAK_ID': '', 'APPIMAGE': '', 'PLAYHEADTV_ROOT': ''}, '/somewhere/client'), InstallKind.unknown);
    });

    test('only the tar.gz install and the AppImage are managed by the updater', () {
      expect({for (final k in InstallKind.values) k: k.managedByUpdater},
          {InstallKind.tarball: true, InstallKind.appImage: true, InstallKind.systemPackage: false, InstallKind.sandbox: false, InstallKind.unknown: false});
    });
  });

  group('banner per install kind', () {
    UpdateBanner? banner(InstallKind kind, {bool auto = true, String running = '1.0.0', String latest = '1.1.0', String? dismissed, String? pending}) =>
        bannerFor(
          kind: kind,
          autoUpdate: auto,
          running: v(running),
          latest: v(latest),
          dismissed: dismissed == null ? null : v(dismissed),
          pending: pending == null ? null : v(pending),
        );

    test('nothing for the same or an older release', () {
      for (final kind in InstallKind.values) {
        expect(banner(kind, latest: '1.0.0'), isNull, reason: '$kind');
        expect(banner(kind, latest: '0.9.0'), isNull, reason: '$kind');
      }
    });

    test('tar.gz / AppImage with auto-update on: it will apply on the next start', () {
      for (final kind in [InstallKind.tarball, InstallKind.appImage]) {
        final b = banner(kind)!;
        expect(b.version, v('1.1.0'));
        expect(b.message, contains('1.1.0'));
        expect(b.message, contains('next time Playhead TV starts'), reason: '$kind');
      }
    });

    test('tar.gz / AppImage with auto-update off: how to apply it, per format', () {
      final tar = banner(InstallKind.tarball, auto: false)!.message;
      expect(tar, contains('auto_update'));
      expect(tar, contains('install.sh'));
      final img = banner(InstallKind.appImage, auto: false)!.message;
      expect(img, contains('auto_update'));
      expect(img, contains('AppImage'));
      expect(img, isNot(contains('install.sh')));
    });

    test('an update already installed and waiting for a restart only says to restart', () {
      for (final kind in [InstallKind.tarball, InstallKind.appImage]) {
        final b = banner(kind, pending: '1.1.0')!;
        expect(b.message, contains('Restart'), reason: '$kind');
        expect(b.message, contains('1.1.0'));
      }
      // An even newer release than the pending one is a normal banner.
      expect(banner(InstallKind.tarball, pending: '1.1.0', latest: '1.2.0')!.message, contains('next time'));
      // An unmanaged install has no pending update.
      expect(banner(InstallKind.systemPackage, pending: '1.1.0')!.message, isNot(contains('Restart')));
    });

    test('a system package points at the package manager, whatever auto_update says', () {
      for (final auto in [true, false]) {
        final m = banner(InstallKind.systemPackage, auto: auto)!.message;
        expect(m, contains('package manager'));
        expect(m, isNot(contains('automatically')));
      }
    });

    test('a sandbox points at the tool that installed it', () {
      expect(banner(InstallKind.sandbox)!.message, contains('flatpak update'));
    });

    test('an unknown install gets the generic message', () {
      final m = banner(InstallKind.unknown)!.message;
      expect(m, contains('releases page'));
      expect(m, isNot(contains('automatically')));
    });

    test('a dismissed version is not shown again until a newer one appears', () {
      expect(banner(InstallKind.unknown, dismissed: '1.1.0'), isNull);
      expect(banner(InstallKind.unknown, dismissed: '1.2.0'), isNull);
      expect(banner(InstallKind.unknown, latest: '1.2.0', dismissed: '1.1.0')!.version, v('1.2.0'));
    });
  });

  group('notice controller', () {
    late Directory scratch;
    late FakeReleaseSource server;
    late MemoryStore store;
    late File stateFile;

    setUp(() {
      scratch = Directory.systemTemp.createTempSync('playheadtv_notices_');
      stateFile = File('${scratch.path}/state.json');
      server = FakeReleaseSource();
      store = MemoryStore();
    });
    tearDown(() => scratch.deleteSync(recursive: true));

    UpdateNoticeController controller({
      String running = '1.0.0',
      InstallKind kind = InstallKind.tarball,
      bool auto = true,
      bool withSource = true,
      Duration bannerFor = const Duration(seconds: 30),
    }) =>
        UpdateNoticeController(
          environment: UpdateNoticeEnvironment(
            runningVersion: running,
            kind: kind,
            stateFile: stateFile,
            releaseSource: withSource ? server : null,
            autoUpdate: () => auto,
            bannerDuration: bannerFor,
            startDelay: Duration.zero,
          ),
          store: store,
        );

    Future<void> publish(String version) async => server.publish(await buildRelease(scratch, version));
    void recordUpdate(String version, {String notes = 'Fixed a thing.'}) => UpdateState(
          lastUpdate: LastUpdate(version: version, previous: '1.0.0', notes: notes, installedAt: DateTime.utc(2030)),
        ).write(stateFile);

    test('a newer release shows the banner for the install kind', () async {
      await publish('1.1.0');
      final c = controller();
      addTearDown(c.dispose);
      await c.start();
      expect(c.state.value!.kind, UpdateOverlayKind.banner);
      expect(c.state.value!.version, v('1.1.0'));
      expect(c.state.value!.body, contains('next time Playhead TV starts'));
    });

    test('Select dismisses the version for good, Back only closes it', () async {
      await publish('1.1.0');
      final c = controller();
      addTearDown(c.dispose);
      await c.start();
      c.close();
      expect(c.state.value, isNull);
      expect(store.dismissedVersion, isNull, reason: 'Back is not a dismissal');

      await c.start();
      expect(c.state.value, isNotNull, reason: 'shown again on the next start');
      c.select();
      expect(c.state.value, isNull);
      expect(store.dismissedVersion, v('1.1.0'));

      final next = controller();
      addTearDown(next.dispose);
      await next.start();
      expect(next.state.value, isNull, reason: 'the same version is not shown again');

      await publish('1.2.0');
      await next.start();
      expect(next.state.value!.version, v('1.2.0'), reason: 'a newer release is');
    });

    test('an unreachable source, a 404 or no release is silent', () async {
      final c = controller();
      addTearDown(c.dispose);
      await c.start(); // nothing published
      expect(c.state.value, isNull);

      await publish('1.1.0');
      server.offline = true;
      await c.start();
      expect(c.state.value, isNull);
    });

    test('up to date, no source and a development build show nothing', () async {
      await publish('1.0.0');
      final upToDate = controller();
      addTearDown(upToDate.dispose);
      await upToDate.start();
      expect(upToDate.state.value, isNull);

      await publish('1.1.0');
      final noSource = controller(withSource: false);
      addTearDown(noSource.dispose);
      await noSource.start();
      expect(noSource.state.value, isNull);

      final dev = controller(running: '0.0.0-dev');
      addTearDown(dev.dispose);
      await dev.start();
      expect(dev.state.value, isNull);
      expect(server.latestCalls, 1, reason: 'only the up-to-date case asked: no source and a development build never do');
    });

    test('an unmanaged install gets its own instruction', () async {
      await publish('1.1.0');
      final c = controller(kind: InstallKind.systemPackage);
      addTearDown(c.dispose);
      await c.start();
      expect(c.state.value!.body, contains('package manager'));
    });

    test('the banner goes away on its own without being remembered as dismissed', () async {
      await publish('1.1.0');
      final c = controller(bannerFor: const Duration(milliseconds: 30));
      addTearDown(c.dispose);
      await c.start();
      expect(c.state.value, isNotNull);
      await Future<void>.delayed(const Duration(milliseconds: 120));
      expect(c.state.value, isNull);
      expect(store.dismissedVersion, isNull);
    });

    test('after an update the changelog is shown once, as plain text', () async {
      recordUpdate('1.1.0', notes: '- Fixed <b>things</b>\n- [link](https://example.test)');
      final c = controller(running: '1.1.0');
      addTearDown(c.dispose);
      await c.start();
      final o = c.state.value!;
      expect(o.kind, UpdateOverlayKind.changelog);
      expect(o.title, 'Updated to 1.1.0');
      expect(o.body, '- Fixed <b>things</b>\n- [link](https://example.test)', reason: 'verbatim text, no interpretation');
      expect(store.changelogShownFor, '1.1.0');

      c.select();
      expect(c.state.value, isNull);
      final again = controller(running: '1.1.0');
      addTearDown(again.dispose);
      await again.start();
      expect(again.state.value, isNull, reason: 'one-time');
    });

    test('the changelog is for the running version only, and an empty one says so', () async {
      recordUpdate('1.1.0');
      final other = controller(running: '1.0.0');
      addTearDown(other.dispose);
      await other.start();
      expect(other.state.value?.kind, isNot(UpdateOverlayKind.changelog));

      recordUpdate('1.1.0', notes: '');
      final c = controller(running: '1.1.0');
      addTearDown(c.dispose);
      await c.start();
      expect(c.state.value!.body, contains('No changelog'));
    });

    test('a corrupt or missing state file shows nothing', () async {
      final c = controller(running: '1.1.0', withSource: false);
      addTearDown(c.dispose);
      await c.start();
      expect(c.state.value, isNull);
      stateFile.writeAsStringSync('{broken');
      await c.start();
      expect(c.state.value, isNull);
    });

    test('an update waiting for a restart makes the banner say so', () async {
      UpdateState(pending: PendingUpdate(version: '1.1.0', previous: '1.0.0', startedAt: DateTime.utc(2030))).write(stateFile);
      await publish('1.1.0');
      final c = controller();
      addTearDown(c.dispose);
      await c.start();
      expect(c.state.value!.body, contains('Restart'));
    });
  });

  group('update state file location', () {
    test('the launcher root, else the AppImage state dir, else none', () {
      expect(updateStateFile({'PLAYHEADTV_ROOT': '/r'})!.path, '/r/state.json');
      expect(updateStateFile({'APPIMAGE': '/a.AppImage', 'HOME': '/h'})!.path, '/h/.local/state/playheadtv/state.json');
      expect(updateStateFile({'HOME': '/h'}), isNull);
    });

    test('an AppImage confirms its healthy start in its own state file', () async {
      final scratch = Directory.systemTemp.createTempSync('playheadtv_confirm_img_');
      addTearDown(() => scratch.deleteSync(recursive: true));
      final state = File('${scratch.path}/.local/state/playheadtv/state.json');
      UpdateState(pending: PendingUpdate(version: '1.1.0', previous: '1.0.0', startedAt: DateTime.now().toUtc())).write(state);
      final timer = scheduleHealthConfirmation(
        env: {'APPIMAGE': '/a.AppImage', 'HOME': scratch.path},
        window: const Duration(milliseconds: 20),
        version: '1.1.0',
      );
      expect(timer, isNotNull);
      await Future<void>.delayed(const Duration(milliseconds: 200));
      expect(UpdateState.read(state).pending!.confirmed, isTrue);
    });
  });
}
