package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// presign 按 AWS Signature V4 为 S3 对象签发预签名 URL。
//
// 只支持 S3 兼容协议所需的子集：host 单头签名、UNSIGNED-PAYLOAD，
// 因此不需要额外依赖，也无需联网对时。
func (s *Store) presign(method, key string, ttl time.Duration) (string, error) {
	endpoint, err := url.Parse(s.opts.Endpoint)
	if err != nil {
		return "", fmt.Errorf("S3 端点非法: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return "", fmt.Errorf("S3 端点缺少协议前缀（http/https）: %s", s.opts.Endpoint)
	}

	host := endpoint.Host
	prefix := strings.TrimRight(endpoint.Path, "/")
	var canonicalURI string
	if s.opts.ForcePathStyle {
		// path-style：桶名进路径，MinIO 等自建服务用这种形态
		canonicalURI = prefix + "/" + escapePath(s.opts.Bucket) + "/" + escapePath(key)
	} else {
		// virtual-hosted style：桶名进主机名；端点已带桶名时不重复拼接
		if !strings.HasPrefix(host, s.opts.Bucket+".") {
			host = s.opts.Bucket + "." + host
		}
		canonicalURI = prefix + "/" + escapePath(key)
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := dateStamp + "/" + s.opts.Region + "/s3/aws4_request"

	query := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    s.opts.AccessKey + "/" + scope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       fmt.Sprintf("%d", int64(ttl/time.Second)),
		"X-Amz-SignedHeaders": "host",
	}
	canonicalQuery := canonicalQueryString(query)

	canonicalHeaders := "host:" + host + "\n"

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256(signingKey(s.opts.SecretKey, dateStamp, s.opts.Region), []byte(stringToSign)))

	base := endpoint.Scheme + "://" + host
	return base + canonicalURI + "?" + canonicalQuery + "&X-Amz-Signature=" + signature, nil
}

// canonicalQueryString 按字典序拼接查询串，键与值都做 SigV4 百分号编码。
func canonicalQueryString(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, escape(k)+"="+escape(params[k]))
	}
	return strings.Join(parts, "&")
}

// escape 按 RFC 3986 编码，斜杠也编码（SigV4 要求）。
func escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// hmacSHA256 计算 HMAC-SHA256。
func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// hexSHA256 计算 SHA-256 的十六进制串。
func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// signingKey 派生 SigV4 的签名密钥。
func signingKey(secret, dateStamp, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}
