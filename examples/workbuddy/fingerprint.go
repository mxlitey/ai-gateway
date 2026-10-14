package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"time"
)

// 设备指纹：以 uid 稳定派生机器码与会话号。
//
// 上游按设备维度做风控，随机机器码反而容易被判异常；用 uid 稳定派生可以保证
// 「同一账号永远来自同一台设备」，多账号之间也天然隔离。
// 注意：md5 的十六进制是 32 位，这里与官方实现取同样的值。
func deriveID(uid, salt string) string {
	if uid == "" {
		uid = "anonymous"
	}
	sum := md5.Sum([]byte(salt + ":" + uid))
	return hex.EncodeToString(sum[:])
}

// requestID 生成带稳定前缀与微秒后缀的 X-Request-ID。
func requestID(uid string) string {
	suffix := fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
	return deriveID(uid, "req") + "-" + suffix
}