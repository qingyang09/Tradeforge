package secretcrypto

import (
	"testing"
	"unicode/utf8"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	ciphertext, salt, nonce, err := Encrypt("correct horse battery staple", "sk-真实密钥-1234")
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	if len(ciphertext) == 0 || len(salt) == 0 || len(nonce) == 0 {
		t.Fatalf("密文/盐/nonce 不应为空：ciphertext=%d salt=%d nonce=%d", len(ciphertext), len(salt), len(nonce))
	}

	got, err := Decrypt("correct horse battery staple", ciphertext, salt, nonce)
	if err != nil {
		t.Fatalf("解密失败：%v", err)
	}
	if got != "sk-真实密钥-1234" {
		t.Errorf("解密结果 = %q，期望 %q", got, "sk-真实密钥-1234")
	}
}

func TestDecryptFailsWithWrongPassword(t *testing.T) {
	ciphertext, salt, nonce, err := Encrypt("password-A", "sk-秘密")
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	if _, err := Decrypt("password-B", ciphertext, salt, nonce); err == nil {
		t.Error("用错误的密码解密应该报错，而不是解出一串垃圾数据")
	}
}

// TestDecryptRejectsMalformedNonceWithoutPanicking 是一次真实事故的回归测试：
// crypto/cipher 的 GCM.Open 在 nonce 长度不对时是 panic，不是返回 error（标准库 AEAD
// 接口的既有行为）。一条损坏的数据曾经直接把调用方启动时的恢复逻辑崩掉。
func TestDecryptRejectsMalformedNonceWithoutPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nonce 长度不对时不应该 panic，应该返回 error；实际 panic：%v", r)
		}
	}()

	salt := make([]byte, saltLen)
	_, err := Decrypt("任意密码", []byte{0x01, 0x02, 0x03}, salt, []byte{0xaa, 0xbb, 0xcc})
	if err == nil {
		t.Error("nonce 长度不对（GCM 标准是 12 字节）应该报错")
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
		t.Error("每次加密都应该用新的盐/nonce，相同明文不应产生相同密文")
	}
}

func TestMaskAPIKey(t *testing.T) {
	if got := MaskAPIKey("short"); got != "••••" {
		t.Errorf("短字符串应该完全遮盖，实际 %q", got)
	}
	if got := MaskAPIKey("sk-ant-testkey-1234"); got != "sk-a…1234" {
		t.Errorf("MaskAPIKey = %q，期望 sk-a…1234", got)
	}
}

// TestMaskAPIKeyProducesValidUTF8ForMultiByteInput 是一次真机测试事故的回归测试：
// 按字节切片（而不是按 rune）在多字节 UTF-8 字符中间切断，会产生非法 UTF-8 序列，
// 存进 Postgres 的 TEXT 列时直接报 "invalid byte sequence for encoding UTF8"，
// 保存交易所配置整个失败。真实的交易所 key 都是纯 ASCII，但输入框不会校验这一点。
func TestMaskAPIKeyProducesValidUTF8ForMultiByteInput(t *testing.T) {
	got := MaskAPIKey("test-api-key-真机")
	if !utf8.ValidString(got) {
		t.Errorf("MaskAPIKey(%q) = %q，产生了非法 UTF-8 序列", "test-api-key-真机", got)
	}
}
