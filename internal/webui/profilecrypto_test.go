package webui

import "testing"

func TestEncryptDecryptProfileSecretRoundTrip(t *testing.T) {
	ciphertext, salt, nonce, err := encryptProfileSecret("correct horse battery staple", "sk-真实密钥-1234")
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	if len(ciphertext) == 0 || len(salt) == 0 || len(nonce) == 0 {
		t.Fatalf("密文/盐/nonce 不应为空：ciphertext=%d salt=%d nonce=%d", len(ciphertext), len(salt), len(nonce))
	}

	got, err := decryptProfileSecret("correct horse battery staple", ciphertext, salt, nonce)
	if err != nil {
		t.Fatalf("解密失败：%v", err)
	}
	if got != "sk-真实密钥-1234" {
		t.Errorf("解密结果 = %q，期望 %q", got, "sk-真实密钥-1234")
	}
}

func TestDecryptProfileSecretFailsWithWrongPassword(t *testing.T) {
	ciphertext, salt, nonce, err := encryptProfileSecret("password-A", "sk-秘密")
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	if _, err := decryptProfileSecret("password-B", ciphertext, salt, nonce); err == nil {
		t.Error("用错误的密码解密应该报错，而不是解出一串垃圾数据")
	}
}

// TestDecryptProfileSecretRejectsMalformedNonceWithoutPanicking 是一次真实事故的回归测试：
// crypto/cipher 的 GCM.Open 在 nonce 长度不对时是 panic，不是返回 error（标准库 AEAD 接口的
// 既有行为）。一条数据损坏的 agent_profiles 记录（比如集成测试留下的假数据）曾经直接把
// cmd/webui 启动时的 LoadActiveAgentProfile 崩掉，整个进程起不来。这里必须是返回 error。
func TestDecryptProfileSecretRejectsMalformedNonceWithoutPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nonce 长度不对时不应该 panic，应该返回 error；实际 panic：%v", r)
		}
	}()

	salt := make([]byte, scryptSaltLen)
	_, err := decryptProfileSecret("任意密码", []byte{0x01, 0x02, 0x03}, salt, []byte{0xaa, 0xbb, 0xcc})
	if err == nil {
		t.Error("nonce 长度不对（GCM 标准是 12 字节）应该报错")
	}
}

func TestEncryptProfileSecretProducesDifferentCiphertextEachTime(t *testing.T) {
	c1, _, _, err := encryptProfileSecret("同一个密码", "同一个明文")
	if err != nil {
		t.Fatal(err)
	}
	c2, _, _, err := encryptProfileSecret("同一个密码", "同一个明文")
	if err != nil {
		t.Fatal(err)
	}
	if string(c1) == string(c2) {
		t.Error("每次加密都应该用新的盐/nonce，相同明文不应产生相同密文")
	}
}
