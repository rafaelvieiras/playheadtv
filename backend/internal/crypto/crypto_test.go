package crypto

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func testKey(fill byte) [32]byte {
	var k [32]byte
	for i := range k {
		k[i] = fill
	}
	return k
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := testKey(1)
	plaintext := []byte("minha-api-key-secreta")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext equals plaintext; not actually encrypted")
	}

	got, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Decrypt round-trip = %q, want %q", got, plaintext)
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	right := testKey(1)
	wrong := testKey(2)

	ciphertext, err := Encrypt(right, []byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(wrong, ciphertext); err == nil {
		t.Fatal("Decrypt with wrong key succeeded, want error")
	}
}

func TestDecryptTamperedCiphertextFails(t *testing.T) {
	key := testKey(1)
	ciphertext, err := Encrypt(key, []byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	tampered := bytes.Clone(ciphertext)
	tampered[len(tampered)-1] ^= 0xFF // flip one bit of the auth tag/ciphertext tail

	if _, err := Decrypt(key, tampered); err == nil {
		t.Fatal("Decrypt with tampered ciphertext succeeded, want error")
	}
}

func TestDecryptShortCiphertextFails(t *testing.T) {
	key := testKey(1)
	if _, err := Decrypt(key, []byte("short")); err == nil {
		t.Fatal("Decrypt with too-short ciphertext succeeded, want error")
	}
}

func TestLoadOrGenerateKeyGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "encryption.key")

	key1, err := LoadOrGenerateKey(keyPath)
	if err != nil {
		t.Fatalf("LoadOrGenerateKey (generate): %v", err)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file perm = %o, want 0600", perm)
	}

	key2, err := LoadOrGenerateKey(keyPath)
	if err != nil {
		t.Fatalf("LoadOrGenerateKey (reload): %v", err)
	}
	if key1 != key2 {
		t.Fatal("LoadOrGenerateKey did not reload the same persisted key")
	}
}

func TestLoadOrGenerateKeyFromEnv(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "encryption.key")

	want := testKey(7)
	t.Setenv(encryptionKeyEnvVar, base64.StdEncoding.EncodeToString(want[:]))

	got, err := LoadOrGenerateKey(keyPath)
	if err != nil {
		t.Fatalf("LoadOrGenerateKey (env): %v", err)
	}
	if got != want {
		t.Fatal("LoadOrGenerateKey did not honor the env var key")
	}
	if _, err := os.Stat(keyPath); err == nil {
		t.Fatal("LoadOrGenerateKey wrote a key file even though the env var was set")
	}
}
