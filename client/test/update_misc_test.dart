import 'dart:convert';
import 'dart:io';

import 'package:client/config/config_file.dart';
import 'package:client/update/app_update.dart';
import 'package:client/update/compat.dart';
import 'package:client/update/install_layout.dart';
import 'package:client/update/release_source.dart';
import 'package:client/update/semver.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

import 'support/update_fakes.dart';

void main() {
  group('SemVer', () {
    test('parses and orders', () {
      SemVer v(String s) => SemVer.tryParse(s)!;
      expect(v('1.2.3').toString(), '1.2.3');
      expect(v('v1.2.3'), v('1.2.3'));
      expect(v('1.2.3+7'), v('1.2.3'));
      expect(v('1.10.0') > v('1.9.9'), isTrue);
      expect(v('2.0.0') > v('1.99.99'), isTrue);
      expect(v('1.0.0') > v('1.0.0-rc.1'), isTrue);
      expect(v('1.0.0-rc.2') > v('1.0.0-rc.1'), isTrue);
      expect(v('1.0.0-rc.10') > v('1.0.0-rc.9'), isTrue);
      expect(v('1.0.0-alpha') > v('1.0.0-1'), isTrue);
      expect(v('1.0.0-rc').isPrerelease, isTrue);
    });

    test('rejects what is not a version', () {
      for (final bad in ['', '1', '1.2', 'a.b.c', '1.2.3.4', '0.0.0-dev!', 'latest']) {
        expect(SemVer.tryParse(bad), isNull, reason: bad);
      }
    });
  });

  group('GitHub releases', () {
    Map<String, Object?> release(String tag, {bool draft = false, bool pre = false, bool assets = true}) {
      final v = tag.replaceFirst('client-v', '');
      return {
        'tag_name': tag,
        'draft': draft,
        'prerelease': pre,
        'assets': assets
            ? [
                {'name': 'playheadtv-client-$v-linux-x64.tar.gz', 'browser_download_url': 'https://dl.example.test/$v.tar.gz'},
                {'name': 'playheadtv-client-$v-checksums.txt', 'browser_download_url': 'https://dl.example.test/$v.sums'},
              ]
            : [],
      };
    }

    test('picks the highest stable client release, ignoring backend, draft, prerelease and incomplete ones', () {
      final picked = pickLatestClientRelease([
        release('v9.0.0'), // a backend release, never a client update
        release('client-v1.2.0'),
        release('client-v1.10.0'),
        release('client-v2.0.0', draft: true),
        release('client-v2.1.0', pre: true),
        release('client-v3.0.0', assets: false),
        release('client-v1.9.0'),
        'garbage',
        {'tag_name': 'client-vnope'},
      ])!;
      expect(picked.version.toString(), '1.10.0');
      expect(picked.packageUrl.toString(), 'https://dl.example.test/1.10.0.tar.gz');
      expect(picked.checksumsUrl.toString(), 'https://dl.example.test/1.10.0.sums');
    });

    test('no client release is null; a non-list body is null', () {
      expect(pickLatestClientRelease([release('v1.0.0')]), isNull);
      expect(pickLatestClientRelease({'message': 'rate limited'}), isNull);
      expect(pickLatestClientRelease(null), isNull);
    });

    test('latestStable lists releases over HTTPS and fails on a bad status', () async {
      Uri? asked;
      final ok = GithubReleaseSource('owner/repo', client: MockClient((req) async {
        asked = req.url;
        return http.Response(jsonEncode([release('client-v1.0.1')]), 200);
      }));
      expect((await ok.latestStable())!.version.toString(), '1.0.1');
      expect(asked!.scheme, 'https');
      expect(asked!.path, '/repos/owner/repo/releases');

      final limited = GithubReleaseSource('owner/repo', client: MockClient((_) async => http.Response('{}', 403)));
      expect(limited.latestStable(), throwsA(isA<HttpException>()));
    });

    test('download writes the body, and a bad status throws', () async {
      final dir = Directory.systemTemp.createTempSync('playheadtv_dl_');
      addTearDown(() => dir.deleteSync(recursive: true));
      final src = GithubReleaseSource('o/r', client: MockClient.streaming((req, _) async {
        return http.StreamedResponse(Stream.value([1, 2, 3]), req.url.path == '/missing' ? 404 : 200);
      }));
      final dest = File('${dir.path}/f.bin');
      await src.download(Uri.parse('https://dl.example.test/ok'), dest);
      expect(dest.readAsBytesSync(), [1, 2, 3]);
      expect(src.download(Uri.parse('https://dl.example.test/missing'), dest), throwsA(isA<HttpException>()));
    });
  });

  group('backend compatibility', () {
    test('classifies against the supported range', () {
      expect(classifyApiVersion(minSupportedApiVersion).status, CompatStatus.compatible);
      expect(classifyApiVersion(maxSupportedApiVersion).status, CompatStatus.compatible);
      final tooNew = classifyApiVersion(maxSupportedApiVersion + 1);
      expect(tooNew.status, CompatStatus.backendTooNew);
      expect(tooNew.isIncompatible, isTrue);
      expect(tooNew.message, contains('Update the app'));
      final tooOld = classifyApiVersion(minSupportedApiVersion - 1);
      expect(tooOld.status, CompatStatus.backendTooOld);
      expect(tooOld.message, contains('Update the backend'));
      expect(classifyApiVersion(minSupportedApiVersion).message, isNull);
    });

    test('parses GET /version, tolerating anything else as unknown', () {
      expect(parseVersionBody('{"version":"1.2.3","api_version":1}').status, CompatStatus.compatible);
      expect(parseVersionBody('{"version":"1.2.3","api_version":1}').backendVersion, '1.2.3');
      expect(parseVersionBody('{"api_version":999}').status, CompatStatus.backendTooNew);
      for (final bad in ['', 'ok', '{}', '[]', '{"api_version":"1"}', '<html>']) {
        expect(parseVersionBody(bad).status, CompatStatus.unknown, reason: bad);
      }
    });

    test('checkBackendCompat never throws: unreachable, 404 and empty URL are unknown', () async {
      final ok = MockClient((r) async => http.Response('{"version":"1.0.0","api_version":${maxSupportedApiVersion + 1}}', 200));
      expect((await checkBackendCompat('https://tv.example.test/', client: ok)).status, CompatStatus.backendTooNew);
      final notFound = MockClient((_) async => http.Response('nope', 404));
      expect((await checkBackendCompat('https://tv.example.test', client: notFound)).status, CompatStatus.unknown);
      final down = MockClient((_) async => throw const SocketException('refused'));
      expect((await checkBackendCompat('https://tv.example.test', client: down)).status, CompatStatus.unknown);
      expect((await checkBackendCompat('')).status, CompatStatus.unknown);
    });
  });

  group('config file', () {
    test('location: explicit path, then XDG, then HOME', () {
      expect(configFileLocation({'PLAYHEADTV_CONFIG': '/x/c.json', 'HOME': '/h'})!.path, '/x/c.json');
      expect(configFileLocation({'XDG_CONFIG_HOME': '/xdg', 'HOME': '/h'})!.path, '/xdg/playheadtv/config.json');
      expect(configFileLocation({'HOME': '/h'})!.path, '/h/.config/playheadtv/config.json');
      expect(configFileLocation({}), isNull);
    });

    test('parse is lenient: anything but a JSON object is empty', () {
      expect(parseConfig('{"playhead_base_url":"https://tv.example.test"}')['playhead_base_url'], 'https://tv.example.test');
      expect(parseConfig('not json'), isEmpty);
      expect(parseConfig('[1,2]'), isEmpty);
      expect(parseConfig(''), isEmpty);
    });

    test('readConfigFile: missing file and null are empty', () {
      expect(readConfigFile(File('/nonexistent/playheadtv.json')), isEmpty);
      expect(readConfigFile(null), isEmpty);
      final dir = Directory.systemTemp.createTempSync('playheadtv_cfg_');
      addTearDown(() => dir.deleteSync(recursive: true));
      final f = File('${dir.path}/config.json')..writeAsStringSync('{"working_set_size": 9}');
      expect(readConfigFile(f)['working_set_size'], 9);
    });

    test('string precedence: override, define, file, empty', () {
      const file = {'k': 'from-file'};
      expect(resolveStringSetting(override: 'o', define: 'd', file: file, key: 'k'), 'o');
      expect(resolveStringSetting(override: '', define: 'd', file: file, key: 'k'), '', reason: 'a test override is final');
      expect(resolveStringSetting(override: null, define: 'd', file: file, key: 'k'), 'd');
      expect(resolveStringSetting(override: null, define: '', file: file, key: 'k'), 'from-file');
      expect(resolveStringSetting(override: null, define: '', file: const {}, key: 'k'), '');
      expect(resolveStringSetting(override: null, define: '', file: const {'k': 5}, key: 'k'), '', reason: 'wrong type');
    });

    test('int precedence and validation', () {
      int r({int? o, bool def = false, int d = 0, Map<String, Object?> f = const {}}) =>
          resolveIntSetting(override: o, defined: def, define: d, file: f, key: 'k', fallback: 5);
      expect(r(o: 2, def: true, d: 3, f: {'k': 4}), 2);
      expect(r(def: true, d: 3, f: {'k': 4}), 3);
      expect(r(f: {'k': 4}), 4);
      expect(r(f: {'k': 0}), 5);
      expect(r(f: {'k': 'x'}), 5);
      expect(r(), 5);
    });
  });

  group('health confirmation', () {
    test('confirms the pending update once the window passes, only under a launcher root', () async {
      final scratch = Directory.systemTemp.createTempSync('playheadtv_confirm_');
      addTearDown(() => scratch.deleteSync(recursive: true));
      final layout = installRoot(Directory('${scratch.path}/root')..createSync(), '1.1.0');
      UpdateState(pending: PendingUpdate(version: '1.1.0', previous: '1.0.0', startedAt: DateTime.now().toUtc()))
          .write(layout.stateFile);

      expect(scheduleHealthConfirmation(env: const {}, version: '1.1.0'), isNull, reason: 'not started by the launcher');
      expect(scheduleHealthConfirmation(env: {'PLAYHEADTV_ROOT': layout.root.path}, version: '0.0.0-dev'), isNotNull);

      final timer = scheduleHealthConfirmation(
        env: {'PLAYHEADTV_ROOT': layout.root.path},
        window: const Duration(milliseconds: 20),
        version: '1.1.0',
      );
      expect(timer, isNotNull);
      expect(UpdateState.read(layout.stateFile).pending!.confirmed, isFalse);
      await Future<void>.delayed(const Duration(milliseconds: 200));
      expect(UpdateState.read(layout.stateFile).pending!.confirmed, isTrue);
    });
  });
}
