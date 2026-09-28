// Package secretcrypto 用一个"外部密码"（目前是 webui 的管理员登录密码）派生密钥，
// 给需要落库的敏感字符串（LLM API key、交易所 API key/secret/passphrase）做
// AES-256-GCM 加密。存进数据库的永远是密文——不引入另一个"主密码"概念，复用
// 已有的登录密码当加密材料；登录密码后来改了的话，已保存的密文就再也解不出来，
// 这是刻意的权衡。
//
// 两个调用方（internal/webui 的模型配置、cmd/executor 读取的交易所配置）共用
// 同一套算法和同一个"登录密码"作为密钥材料，所以放在一个不依赖任一方的独立包里，
// 避免 cmd/executor 为了解密不得不引入整个 HTTP server 包。
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
	scryptN   = 1 << 15 // 32768，本地单进程场景下的常见取值，加密/解密一次几十毫秒
	scryptR   = 8
	scryptP   = 1
	aesKeyLen = 32 // AES-256
)

func deriveKey(password string, salt []byte) ([]byte, error) {
	if password == "" {
		return nil, errors.New("没有密码可用于派生加密密钥")
	}
	return scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, aesKeyLen)
}

// Encrypt 用 password 派生出的密钥加密 plaintext，返回密文、本次派生用的盐、
// GCM 用的随机数。三者都要存起来，解密时缺一不可。
func Encrypt(password, plaintext string) (ciphertext, salt, nonce []byte, err error) {
	salt = make([]byte, saltLen)
	if _, err = rand.Read(salt); err != nil {
		return nil, nil, nil, fmt.Errorf("生成盐失败：%w", err)
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return nil, nil, nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("构造 AES cipher 失败：%w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("构造 GCM 失败：%w", err)
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, nil, fmt.Errorf("生成 nonce 失败：%w", err)
	}
	ciphertext = gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return ciphertext, salt, nonce, nil
}

// Decrypt 是 Encrypt 的逆过程。密码错了（比如登录密码后来改过）会在这里报错，
// 而不是解出一串垃圾字节当成 API key/secret 去调用交易所接口。
func Decrypt(password string, ciphertext, salt, nonce []byte) (string, error) {
	key, err := deriveKey(password, salt)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("构造 AES cipher 失败：%w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("构造 GCM 失败：%w", err)
	}
	// gcm.Open 在 nonce 长度不对时是 panic，不是返回 error（标准库 AEAD 接口的既有行为，
	// 调用方必须自己保证长度对得上）——数据库里存的一行如果损坏/被篡改过导致 nonce
	// 长度不对，不能让它直接把整个进程崩掉，这里必须在调用前显式挡一道。
	if len(nonce) != gcm.NonceSize() {
		return "", fmt.Errorf("nonce 长度不对（got %d, want %d），这份配置的数据可能已损坏",
			len(nonce), gcm.NonceSize())
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("解密失败（密码可能变了）：%w", err)
	}
	return string(plaintext), nil
}

// MaskAPIKey 只保留前后几位，供界面确认"当前配置的是哪把 key"，不泄露完整密钥。
//
// 按 rune 切片，不能按字节切片：真实的交易所/LLM key 都是纯 ASCII，但界面输入框
// 不会校验这一点，用户一旦粘贴进非 ASCII 字符（多字节 UTF-8），按字节切片会在
// 一个多字节字符中间切断，产生非法 UTF-8 序列——这段坏字节存进 Postgres 的 TEXT
// 列时会直接报 "invalid byte sequence for encoding UTF8"，保存操作整个失败
// （这不是假设，是真机测试触发过的真实故障）。
func MaskAPIKey(k string) string {
	r := []rune(k)
	if len(r) <= 8 {
		return "••••"
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}
