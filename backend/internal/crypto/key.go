package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
	"os"
)

// encryptionKeyEnvVar overrides the on-disk key with one supplied out of
// band (e.g. a secrets manager in a future cloud-sync deployment).
const encryptionKeyEnvVar = "PLAYHEADTV_ENCRYPTION_KEY"

// LoadOrGenerateKey resolves the AES-256 key used to encrypt data-at-rest
// fields (currently media_source.credential_cipher), in this order:
//
//  1. PLAYHEADTV_ENCRYPTION_KEY, if set: base64 (StdEncoding) decoded, must
//     be exactly 32 bytes.
//  2. keyPath, if it already exists: read and base64-decoded the same way.
//  3. Otherwise: 32 random bytes are generated, written to keyPath as
//     base64 with 0600 permissions, and a loud warning is logged — losing
//     that file means losing the ability to decrypt anything already
//     encrypted under it (nothing else in the database is affected).
//
// This lives in internal/crypto rather than internal/storage because it's
// pure key material handling (env var / file / random-generation), with no
// dependency on storage.DB or SQL — internal/storage stays focused on the
// DB abstraction itself.
func LoadOrGenerateKey(keyPath string) ([32]byte, error) {
	var key [32]byte

	if encoded := os.Getenv(encryptionKeyEnvVar); encoded != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return key, fmt.Errorf("crypto: %s is not valid base64: %w", encryptionKeyEnvVar, err)
		}
		if len(decoded) != 32 {
			return key, fmt.Errorf("crypto: %s must decode to exactly 32 bytes, got %d", encryptionKeyEnvVar, len(decoded))
		}
		copy(key[:], decoded)
		return key, nil
	}

	if data, err := os.ReadFile(keyPath); err == nil {
		decoded, err := base64.StdEncoding.DecodeString(string(data))
		if err != nil {
			return key, fmt.Errorf("crypto: key file %s is not valid base64: %w", keyPath, err)
		}
		if len(decoded) != 32 {
			return key, fmt.Errorf("crypto: key file %s must decode to exactly 32 bytes, got %d", keyPath, len(decoded))
		}
		copy(key[:], decoded)
		return key, nil
	} else if !os.IsNotExist(err) {
		return key, fmt.Errorf("crypto: read key file %s: %w", keyPath, err)
	}

	if _, err := rand.Read(key[:]); err != nil {
		return key, fmt.Errorf("crypto: generate key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key[:])
	if err := os.WriteFile(keyPath, []byte(encoded), 0o600); err != nil {
		return key, fmt.Errorf("crypto: write key file %s: %w", keyPath, err)
	}

	logging.Warnf("⚠ ⚠ ⚠  NEW ENCRYPTION KEY GENERATED  ⚠ ⚠ ⚠")
	logging.Warnf("crypto: no %s set and no key file found; generated a new one at %s", encryptionKeyEnvVar, keyPath)
	logging.Warnf("crypto: BACK UP THIS FILE along with your database. If it is lost, any")
	logging.Warnf("crypto: media_source credential already stored encrypted CANNOT be recovered")
	logging.Warnf("crypto: (the rest of the database is unaffected).")

	return key, nil
}
