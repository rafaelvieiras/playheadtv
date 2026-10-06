// ===========================================================================
// THE PINNED UPDATE PUBLIC KEYS. This is the one place they live.
// ===========================================================================
//
// Every update is authenticated against these keys: the updater verifies the
// Ed25519 signature of a release's checksums file with them (signature.dart)
// before it downloads anything else.
//
// THIS LIST SHIPS EMPTY. A build with no key pinned REFUSES every update
// ("no update signing key is pinned"): it never skips verification silently.
// Before the first signed release, the maintainer generates the key pair and
// puts the PUBLIC key here (docs/RELEASING.md "Signing key"): the base64 of the
// raw 32-byte Ed25519 public key. The private key lives only in the
// repository's CI secret and never in the repository.
//
// Rotation: add the new public key to this list and release (signed with the
// OLD key); once clients run that release, sign with the new key, and drop the
// old key in a later release. A signature is accepted when ANY listed key
// verifies it.
//
// Forks that sign their own releases either edit this list or build with
// `--dart-define=PLAYHEADTV_UPDATE_PUBLIC_KEYS=<key>[,<key>...]`, which takes
// the place of the list when it is not empty.
//
// Development: `--dart-define=PLAYHEADTV_ALLOW_UNSIGNED_UPDATES=true` lets a
// build WITH NO KEY pinned install unsigned releases (for trying the updater
// against a scratch server). The packaging scripts never set it, and the
// release workflow fails when no key is pinned.
import 'signature.dart';

const List<String> pinnedUpdatePublicKeys = <String>[
  // 'BASE64_OF_RAW_32_BYTE_ED25519_PUBLIC_KEY',
];

const _definedKeys = String.fromEnvironment('PLAYHEADTV_UPDATE_PUBLIC_KEYS');
const allowUnsignedUpdatesBuild = bool.fromEnvironment('PLAYHEADTV_ALLOW_UNSIGNED_UPDATES');

/// The keys this build verifies against: the build-time define when set, else
/// [pinnedUpdatePublicKeys].
List<String> effectivePinnedKeys() => _definedKeys.isNotEmpty
    ? _definedKeys.split(',').map((k) => k.trim()).where((k) => k.isNotEmpty).toList()
    : pinnedUpdatePublicKeys;

/// The verifier the real updater uses.
UpdateVerifier defaultUpdateVerifier() =>
    UpdateVerifier(publicKeys: effectivePinnedKeys(), allowUnsigned: allowUnsignedUpdatesBuild);
