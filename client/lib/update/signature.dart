// Release authenticity: an Ed25519 signature over the release's checksums file,
// checked against public keys pinned in the client (`pinned_keys.dart`). The
// checksums file lists the SHA-256 of every downloadable artifact, so one
// signature covers all of them; the per-artifact hash then only has to match
// the signed list. A checksum alone proves integrity, not authenticity: whoever
// can edit a release can edit its checksums file too.
//
// Pure Dart (package:cryptography's DartEd25519), so the headless updater
// executable needs no native library.
import 'dart:convert';

import 'package:cryptography/cryptography.dart';

enum SignatureStatus {
  /// The signature verifies against one of the pinned keys.
  valid,

  /// The release has no signature.
  missing,

  /// The signature is malformed, or verifies against none of the pinned keys
  /// (wrong key, or the checksums file was altered).
  invalid,

  /// The client has no usable public key pinned and unsigned updates were not
  /// explicitly allowed: nothing can be authenticated, so nothing is installed.
  noKeyPinned,
}

class SignatureCheck {
  final SignatureStatus status;
  final String message;
  const SignatureCheck(this.status, this.message);

  /// True when the update may proceed (a valid signature, or the explicit
  /// development switch).
  bool get accepted => status == SignatureStatus.valid;
}

/// Checks the signature of a release's checksums file.
class UpdateVerifier {
  /// Pinned public keys, each the base64 of a raw 32-byte Ed25519 key. A list,
  /// so a new key can be shipped (and the release signed with it) before the
  /// old one is retired: the signature is accepted when ANY key verifies it.
  final List<String> publicKeys;

  /// Development builds only (`--dart-define=PLAYHEADTV_ALLOW_UNSIGNED_UPDATES`,
  /// never set by the packaging scripts): with no key pinned, accept an
  /// unsigned release instead of refusing. Ignored when a key IS pinned.
  final bool allowUnsigned;

  const UpdateVerifier({required this.publicKeys, this.allowUnsigned = false});

  List<SimplePublicKey> get _keys {
    final keys = <SimplePublicKey>[];
    for (final text in publicKeys) {
      try {
        final bytes = base64.decode(text.replaceAll(RegExp(r'\s'), ''));
        if (bytes.length == 32) keys.add(SimplePublicKey(bytes, type: KeyPairType.ed25519));
      } on FormatException {
        // A malformed pinned key is as good as none: it can never verify.
      }
    }
    return keys;
  }

  /// Verifies [signature] (base64 text of the 64-byte signature, or null when
  /// the release has none) over [message], the exact bytes of the checksums
  /// file.
  Future<SignatureCheck> verify(List<int> message, String? signature) async {
    final keys = _keys;
    if (keys.isEmpty) {
      if (allowUnsigned) {
        return const SignatureCheck(SignatureStatus.valid,
            'WARNING: no update key is pinned in this build and unsigned updates are allowed (development build): release NOT authenticated');
      }
      return const SignatureCheck(SignatureStatus.noKeyPinned,
          'no update signing key is pinned in this build, refusing to install an unauthenticated release');
    }
    if (signature == null || signature.trim().isEmpty) {
      return const SignatureCheck(SignatureStatus.missing, 'the release has no signature');
    }
    final List<int> raw;
    try {
      raw = base64.decode(signature.replaceAll(RegExp(r'\s'), ''));
    } on FormatException {
      return const SignatureCheck(SignatureStatus.invalid, 'the signature is not valid base64');
    }
    if (raw.length != 64) {
      return SignatureCheck(SignatureStatus.invalid, 'the signature has ${raw.length} bytes, expected 64');
    }
    final ed25519 = Ed25519();
    for (final key in keys) {
      if (await ed25519.verify(message, signature: Signature(raw, publicKey: key))) {
        return const SignatureCheck(SignatureStatus.valid, 'signature verified');
      }
    }
    return const SignatureCheck(SignatureStatus.invalid, 'the signature does not verify against any pinned key');
  }
}
