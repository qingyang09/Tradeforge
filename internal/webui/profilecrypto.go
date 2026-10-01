package webui

import "tradeforge/internal/secretcrypto"

// LLM API keys / exchange credentials saved to the database are encrypted
// with the admin's login password, never stored as plaintext. The actual
// AES-256-GCM + scrypt implementation lives in internal/secretcrypto -- that
// package depends on neither webui nor storage, and cmd/executor reuses the
// exact same implementation when reading exchange configs, rather than
// reinventing a separate encryption path. This file just keeps function
// names consistent with historical call sites, to avoid a wide-reaching
// rename across every caller.
const scryptSaltLen = 16

func encryptProfileSecret(password, plaintext string) (ciphertext, salt, nonce []byte, err error) {
	return secretcrypto.Encrypt(password, plaintext)
}

func decryptProfileSecret(password string, ciphertext, salt, nonce []byte) (string, error) {
	return secretcrypto.Decrypt(password, ciphertext, salt, nonce)
}

func maskAPIKey(k string) string {
	return secretcrypto.MaskAPIKey(k)
}
