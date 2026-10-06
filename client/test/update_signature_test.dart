// The signature primitives on their own: interoperability with the exact
// OpenSSL commands the release workflow and docs/RELEASING.md use (a TEST key
// generated here, in a temp directory, and thrown away), and the plain-text
// handling of release notes.
import 'dart:convert';
import 'dart:io';

import 'package:client/update/signature.dart';
import 'package:client/update/update_notes.dart';
import 'package:flutter_test/flutter_test.dart';

bool get _hasOpenSsl {
  try {
    return Process.runSync('openssl', ['version']).exitCode == 0;
  } on ProcessException {
    return false;
  }
}

void main() {
  group('OpenSSL interoperability', () {
    late Directory dir;
    setUp(() => dir = Directory.systemTemp.createTempSync('playheadtv_sig_'));
    tearDown(() => dir.deleteSync(recursive: true));

    test('a signature made with the documented openssl command verifies against the documented public key', () async {
      final key = '${dir.path}/test-key.pem';
      expect(Process.runSync('openssl', ['genpkey', '-algorithm', 'ed25519', '-out', key]).exitCode, 0);
      // The public key as pinned in the client: base64 of the raw 32 bytes (the
      // last 32 bytes of the DER SubjectPublicKeyInfo).
      final der = Process.runSync('openssl', ['pkey', '-in', key, '-pubout', '-outform', 'DER'], stdoutEncoding: null);
      final publicKey = base64.encode((der.stdout as List<int>).sublist((der.stdout as List<int>).length - 32));

      final checksums = File('${dir.path}/checksums.txt')..writeAsStringSync('${'ab' * 32}  playheadtv-client-1.0.0-linux-x64.tar.gz\n');
      final signed = Process.runSync(
          'openssl', ['pkeyutl', '-sign', '-rawin', '-inkey', key, '-in', checksums.path, '-out', '${dir.path}/sig.bin'],
          stdoutEncoding: null);
      expect(signed.exitCode, 0, reason: '${signed.stderr}');
      final signature = base64.encode(File('${dir.path}/sig.bin').readAsBytesSync());

      final verifier = UpdateVerifier(publicKeys: [publicKey]);
      expect((await verifier.verify(checksums.readAsBytesSync(), signature)).status, SignatureStatus.valid);
      expect((await verifier.verify(utf8.encode('tampered'), signature)).status, SignatureStatus.invalid);
    }, skip: _hasOpenSsl ? false : 'openssl is not installed');
  });

  group('release notes are plain text', () {
    test('markup and links are kept as literal text, never interpreted', () {
      const body = '## What changed\n- [click](https://example.test/x)\n<script>alert(1)</script>\n**bold**';
      expect(sanitizeNotes(body), body);
    });

    test('control characters and terminal escapes are dropped, line endings normalised', () {
      expect(sanitizeNotes('a\u001b[31mred\u0000\r\nb\rc\td\u202ex'), 'a[31mred\nb\nc    dx');
    });

    test('blank-line runs collapse and the text is trimmed', () {
      expect(sanitizeNotes('\n\n  one\n\n\n\n\ntwo  \n\n'), 'one\n\ntwo');
    });

    test('long notes are truncated with an ellipsis, never mid surrogate pair', () {
      final long = 'x' * 100;
      final cut = sanitizeNotes(long, maxLength: 10);
      expect(cut, '${'x' * 10}\n...');
      final emoji = sanitizeNotes('${'a' * 9}\u{1F600}tail', maxLength: 10);
      expect(emoji.startsWith('a' * 9), isTrue);
      expect(emoji.contains('\uD83D') && !emoji.contains('\u{1F600}'), isFalse, reason: 'no lone high surrogate');
    });

    test('an empty body stays empty', () => expect(sanitizeNotes(''), ''));
  });
}
