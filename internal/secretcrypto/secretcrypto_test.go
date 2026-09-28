package secretcrypto

import (
	"testing"
	"unicode/utf8"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	ciphertext, salt, nonce, err := Encrypt("correct horse battery staple", "sk-真实密钥-1234")
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if len(ciphertext) == 0 || len(salt) == 0 || len(nonce) == 0 {
		t.Fatalf("ciphertext/salt/nonce should not be empty: ciphertext=%d salt=%d nonce=%d", len(ciphertext), len(salt), len(nonce))
	}

	got, err := Decrypt("correct horse battery staple", ciphertext, salt, nonce)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if got != "sk-真实密钥-1234" {
		t.Errorf("decrypted = %q, want %q", got, "sk-真实密钥-1234")
	}
}

func TestDecryptFailsWithWrongPassword(t *testing.T) {
	ciphertext, salt, nonce, err := Encrypt("password-A", "sk-秘密")
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if _, err := Decrypt("password-B", ciphertext, salt, nonce); err == nil {
		t.Error("decrypting with the wrong password should error, not decrypt into a string of garbage data")
	}
}

// TestDecryptRejectsMalformedNonceWithoutPanicking is a regression test for a
// real incident: crypto/cipher's GCM.Open panics on a nonce of the wrong
// length instead of returning an error (existing behavior of the standard
// library's AEAD interface). A piece of corrupted data once crashed a
// caller's startup recovery logic outright.
func TestDecryptRejectsMalformedNonceWithoutPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("should not panic on a nonce of the wrong length, should return an error instead; got panic: %v", r)
		}
	}()

	salt := make([]byte, saltLen)
	_, err := Decrypt("任意密码", []byte{0x01, 0x02, 0x03}, salt, []byte{0xaa, 0xbb, 0xcc})
	if err == nil {
		t.Error("a nonce of the wrong length (GCM standard is 12 bytes) should error")
	}
}

func TestEncryptProducesDifferentCiphertextEachTime(t *testing.T) {
	c1, _, _, err := Encrypt("同一个密码", "同一个明文")
	if err != nil {
		t.Fatal(err)
	}
	c2, _, _, err := Encrypt("同一个密码", "同一个明文")
	if err != nil {
		t.Fatal(err)
	}
	if string(c1) == string(c2) {
		t.Error("each encryption should use a fresh salt/nonce; the same plaintext should not produce the same ciphertext")
	}
}

func TestMaskAPIKey(t *testing.T) {
	if got := MaskAPIKey("short"); got != "••••" {
		t.Errorf("a short string should be fully masked, got %q", got)
	}
	if got := MaskAPIKey("sk-ant-testkey-1234"); got != "sk-a…1234" {
		t.Errorf("MaskAPIKey = %q, want sk-a…1234", got)
	}
}

// TestMaskAPIKeyProducesValidUTF8ForMultiByteInput is a regression test for a
// real on-device incident: slicing by byte (instead of by rune) can cut a
// multi-byte UTF-8 character in half, producing an invalid UTF-8 sequence;
// saving that into a Postgres TEXT column fails outright with "invalid byte
// sequence for encoding UTF8", and the whole exchange-config save fails.
// Real exchange keys are pure ASCII, but the input field doesn't enforce
// that.
func TestMaskAPIKeyProducesValidUTF8ForMultiByteInput(t *testing.T) {
	got := MaskAPIKey("test-api-key-真机")
	if !utf8.ValidString(got) {
		t.Errorf("MaskAPIKey(%q) = %q, produced an invalid UTF-8 sequence", "test-api-key-真机", got)
	}
}
