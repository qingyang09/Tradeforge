package webui

import "tradeforge/internal/secretcrypto"

// 保存到数据库的 LLM API key / 交易所凭据都用管理员登录密码加密，不是明文落库。
// 实际的 AES-256-GCM + scrypt 实现在 internal/secretcrypto 里——那个包不依赖
// webui 或 storage，cmd/executor 读取交易所配置时也复用同一份实现，不重新发明
// 一套加密逻辑。这里只是保留跟历史调用点一致的函数名，避免大范围改动调用方。
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
