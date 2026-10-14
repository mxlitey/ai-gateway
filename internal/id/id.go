// Package id 提供随机标识生成，避免引入额外依赖。
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// UUID 返回 RFC 4122 v4 形式的 UUID 字符串。
func UUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败属于不可恢复的环境问题
		panic("id: 随机源不可用: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}

// Hex 返回 n 字节随机数据的十六进制串（长度 2n）。
func Hex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("id: 随机源不可用: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// APIKey 生成客户端密钥（sk- 前缀 + 48 位十六进制）。
func APIKey() string {
	return "sk-" + Hex(24)
}