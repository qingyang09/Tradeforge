// Package idgen 生成平台使用的标识符。
package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewUUID 生成 RFC 4122 版本 4 的 UUID 字符串。
//
// 自己实现而不是引第三方库：全项目只需要这一个函数，
// 而 Postgres 的 uuid 列要求标准的带连字符格式。
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败意味着系统熵源不可用。此时继续跑会产出可预测的
		// 审计 ID 与订单号，比直接崩溃更危险。
		panic(fmt.Sprintf("idgen: 无法读取随机数生成 UUID：%v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10

	buf := make([]byte, 36)
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf)
}
