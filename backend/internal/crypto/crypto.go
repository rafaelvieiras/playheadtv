// Package crypto provides AES-256-GCM encryption for data-at-rest fields
// (currently just media_source.credential_cipher) and the key management
// used to load or bootstrap the key that encrypts them. It intentionally
// knows nothing about domain types or storage.DB — callers own how the
// ciphertext bytes get persisted.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
)

// Encrypt seals plaintext with AES-256-GCM under key, returning a random
// nonce prefixed to the ciphertext (the idiomatic gcm.Seal(nonce, nonce,
// ...) pattern) so Decrypt doesn't need the nonce passed separately.
func Encrypt(key [32]byte, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt reverses Encrypt: it splits the leading nonce off ciphertext and
// opens the remainder under key. It returns an error if ciphertext is
// shorter than one nonce, or if GCM's authentication tag doesn't match —
// which covers both a tampered ciphertext and a wrong key, since GCM folds
// key-correctness into the same tag check.
func Decrypt(key [32]byte, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("crypto: ciphertext too short (%d bytes, need at least %d)", len(ciphertext), nonceSize)
	}
	nonce, sealed := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: decrypt: %w (tampered ciphertext or wrong key)", err)
	}
	return plaintext, nil
}
