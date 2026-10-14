package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// JWT 工具：只读 payload 里的信息（sub=uid、exp、iss），不校验签名。
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	seg := parts[1]
	if pad := len(seg) % 4; pad != 0 {
		seg += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func jwtUID(token string) string {
	return asString(jwtClaims(token)["sub"])
}

func jwtIssuer(token string) string {
	return asString(jwtClaims(token)["iss"])
}

// jwtExpMillis 把 JWT 的 exp（秒）转成毫秒时间戳。
func jwtExpMillis(token string) int64 {
	if exp := toFloat(jwtClaims(token)["exp"]); exp > 0 {
		return int64(exp) * 1000
	}
	return 0
}

// 区域判定用的字样：国内版历史上换过出口，三个都要认。
var (
	cnRealmMarkers   = []string{"copilot.tencent.com", "codebuddy.cn", "workbuddy.cn"}
	intlRealmMarkers = []string{"workbuddy.ai"}
)

// realmFromToken 从 token 的 issuer（其次域名）判断账号属于哪个区域。
// 判不出来返回空串——文件名只是安装约定，不能作为主依据。
func realmFromToken(token, domain string) string {
	iss := strings.ToLower(jwtIssuer(token))
	for _, m := range cnRealmMarkers {
		if strings.Contains(iss, m) {
			return "cn"
		}
	}
	for _, m := range intlRealmMarkers {
		if strings.Contains(iss, m) {
			return "intl"
		}
	}
	dom := strings.ToLower(domain)
	for _, m := range cnRealmMarkers {
		if strings.Contains(dom, m) {
			return "cn"
		}
	}
	for _, m := range intlRealmMarkers {
		if strings.Contains(dom, m) {
			return "intl"
		}
	}
	return ""
}

// normalizeEpochMillis 把秒或毫秒的时间戳统一成毫秒。
func normalizeEpochMillis(v any) int64 {
	n := toFloat(v)
	if n <= 0 {
		return 0
	}
	if n > 1e11 {
		return int64(n)
	}
	return int64(n * 1000)
}