import 'package:client/main.dart' show AppConfig, jellyfinImageUrl, streamUrl;
import 'package:flutter_test/flutter_test.dart';

void main() {
  setUp(() {
    AppConfig.overrideForTest(
      jellyfinUrl: 'http://jf.example.com',
      jellyfinApiKey: 'k3y',
      playheadBaseUrl: 'http://127.0.0.1:1',
    );
  });

  // Jellyfin 12 disables the legacy `api_key` query parameter by default, so
  // only `ApiKey` may carry the key in the URLs handed to the player and the
  // image loader.
  test('stream URL carries the key as ApiKey, never api_key', () {
    final uri = Uri.parse(streamUrl('abc'));
    expect(uri.path, '/Videos/abc/stream');
    expect(uri.queryParameters['Static'], 'true');
    expect(uri.queryParameters['ApiKey'], 'k3y');
    expect(uri.queryParameters.containsKey('api_key'), isFalse);
  });

  test('image URL carries the key as ApiKey, never api_key', () {
    final uri = Uri.parse(jellyfinImageUrl('abc', 'tag1', maxWidth: 200));
    expect(uri.path, '/Items/abc/Images/Primary');
    expect(uri.queryParameters['tag'], 'tag1');
    expect(uri.queryParameters['maxWidth'], '200');
    expect(uri.queryParameters['ApiKey'], 'k3y');
    expect(uri.queryParameters.containsKey('api_key'), isFalse);
  });
}
