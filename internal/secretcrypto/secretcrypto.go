// Package secretcrypto derives an encryption key from an "external password"
// (currently the webui admin login password) and uses it for AES-256-GCM
// encryption of sensitive strings that need to be persisted (LLM API keys,
// exchange API key/secret/passphrase). What lands in the database is always
// ciphertext — rather than introduce a separate "master password" concept,
// this reuses the existing login password as key material; a deliberate
// tradeoff is that if the login password is later changed, ciphertext
// already saved can no longer be decrypted.
//
// The two callers (internal/webui's model config, and the exchange config
// cmd/executor reads) share the same algorithm and the same "login password"
// as key material, so this lives in its own package that depends on neither,
// keeping cmd/executor from having to pull in the whole HTTP server package
// just to decrypt.
package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/scrypt"
)

const (
	saltLen   = 16
	scryptN   = 1 << 15 // 32768, a common choice for a local single-process setting; one encrypt/decrypt takes tens of milliseconds
	scryptR   = 8
	scryptP   = 1
	aesKeyLen = 32 // AES-256
)

func deriveKey(password string, salt []byte) ([]byte, error) {
	if password == "" {
		return nil, errors.New("no password available to derive an encryption key from")
	}
	return scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, aesKeyLen)
}

// Encrypt encrypts plaintext with a key derived from password, returning the
// ciphertext, the salt used for this derivation, and the nonce used for GCM.
// All three must be stored — decryption needs every one of them.
func Encrypt(password, plaintext string) (ciphertext, salt, nonce []byte, err error) {
	salt = make([]byte, saltLen)
	if _, err = rand.Read(salt); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate salt: %w", err)
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return nil, nil, nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to construct AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to construct GCM: %w", err)
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate nonce: %w", err)
	}
	ciphertext = gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return ciphertext, salt, nonce, nil
}

// Decrypt is the inverse of Encrypt. A wrong password (e.g. the login
// password was changed since) surfaces as an error here, rather than
// decrypting into a string of garbage bytes that then gets used as an API
// key/secret against an exchange's API.
func Decrypt(password string, ciphertext, salt, nonce []byte) (string, error) {
	key, err := deriveKey(password, salt)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to construct AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to construct GCM: %w", err)
	}
	// gcm.Open panics on a nonce of the wrong length instead of returning an
	// error (existing behavior of the standard library's AEAD interface,
	// which requires the caller to guarantee the length itself) — if a row
	// in the database is corrupted or tampered with such that its nonce
	// length is wrong, that must not be allowed to crash the whole process;
	// this has to be explicitly checked before the call.
	if len(nonce) != gcm.NonceSize() {
		return "", fmt.Errorf("nonce has the wrong length (got %d, want %d); this config's data may be corrupted",
			len(nonce), gcm.NonceSize())
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed (the password may have changed): %w", err)
	}
	return string(plaintext), nil
}

// MaskAPIKey keeps only a few characters at each end, enough for the UI to
// confirm "which key is currently configured" without leaking the full key.
//
// It slices by rune, not by byte: real exchange/LLM keys are pure ASCII, but
// the UI's input field doesn't enforce that, and if a user ever pastes in a
// non-ASCII character (multi-byte UTF-8), slicing by byte can cut a
// multi-byte character in half, producing an invalid UTF-8 sequence — saving
// that broken byte sequence into a Postgres TEXT column then fails outright
// with "invalid byte sequence for encoding UTF8", and the whole save
// operation fails (this isn't hypothetical — it's a real failure hit during
// testing on a real device).
func MaskAPIKey(k string) string {
	r := []rune(k)
	if len(r) <= 8 {
		return "••••"
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}
