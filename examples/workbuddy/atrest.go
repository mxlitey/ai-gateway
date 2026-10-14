package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// 桌面端 at-rest 加密（suite 1）的信封解析与解密。
//
// 算法（与客户端一致，AAD 必须逐字节相同，否则 GCM 校验必然失败）：
//
//	key    = sha256(atRestSecretKey 的 44 字符 base64 字符串)
//	keyId  = sha256(key).hex[:16]
//	AAD    = "WB-AAD\0" | 0x01 | LP(magic) | LP("sym-v1") | u32(suite) | LP(keyId) | code | 0x00 | 0x00
//	信封   = base64(JSON{suite,keyId,nonce,authTag,ciphertext})，AES-256-GCM
//
// LP(s) = u32be(utf8 长度) + utf8(s)
const (
	envelopeKeyName = "$wbEncrypted"
	schemeName      = "sym-v1"
	suiteV1         = 1
)

// frame 表示信封所处的层级，两层用了不同的魔数与编码：
// 文件级（keyblob 的 wrapped）与字段级（.info 里的 accessToken 等）。
type frame struct {
	magic string
	code  byte
}

var (
	frameFile  = frame{magic: "WBEF1", code: 1}
	frameField = frame{magic: "WBEV1", code: 2}
)

func lpBytes(s string) []byte {
	raw := []byte(s)
	out := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(out, uint32(len(raw)))
	copy(out[4:], raw)
	return out
}

func u32be(v uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, v)
	return out
}

func buildAAD(keyID string, suite int, f frame) []byte {
	out := []byte("WB-AAD\x00")
	out = append(out, 0x01)
	out = append(out, lpBytes(f.magic)...)
	out = append(out, lpBytes(schemeName)...)
	out = append(out, u32be(uint32(suite))...)
	out = append(out, lpBytes(keyID)...)
	out = append(out, f.code, 0x00, 0x00)
	return out
}

// DeriveKeyID 返回 sha256(key) 的前 16 个 hex，即客户端口中的 keyId。
func DeriveKeyID(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])[:16]
}

type envelope struct {
	suite      int
	keyID      string
	nonce      []byte
	authTag    []byte
	ciphertext []byte
}

// IsEnvelope 判断某个 JSON 值是不是 $wbEncrypted 信封（而不是明文）。
func IsEnvelope(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t["envelope"]; !ok {
			return false
		}
		_, has := t[envelopeKeyName]
		return has
	case string:
		s := strings.TrimLeft(t, " \t\r\n")
		if !strings.HasPrefix(s, "{") {
			return false
		}
		head := s
		if len(head) > 64 {
			head = head[:64]
		}
		return strings.Contains(head, envelopeKeyName)
	}
	return false
}

func parseEnvelope(v any) (*envelope, error) {
	raw := ""
	switch t := v.(type) {
	case map[string]any:
		raw, _ = t["envelope"].(string)
	case string:
		raw = t
	}
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("信封为空")
	}

	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("信封不是合法 base64: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(decoded, &m); err != nil {
		return nil, fmt.Errorf("信封不是合法 JSON: %w", err)
	}

	env := &envelope{suite: suiteV1}
	if s, ok := m["suite"].(float64); ok && s > 0 {
		env.suite = int(s)
	}
	env.keyID = strings.ToLower(strings.TrimSpace(asString(m["keyId"])))

	for name, dst := range map[string]*[]byte{
		"nonce":      &env.nonce,
		"authTag":    &env.authTag,
		"ciphertext": &env.ciphertext,
	} {
		b, err := base64.StdEncoding.DecodeString(asString(m[name]))
		if err != nil {
			return nil, fmt.Errorf("信封字段 %s 不是合法 base64: %w", name, err)
		}
		*dst = b
	}
	if len(env.nonce) != 12 || len(env.authTag) != 16 {
		return nil, fmt.Errorf("信封 nonce(%d)/authTag(%d) 长度不符", len(env.nonce), len(env.authTag))
	}
	return env, nil
}

// OpenEnvelope 用 key 解开一个信封，返回明文字节。
func OpenEnvelope(key []byte, v any, f frame) ([]byte, error) {
	env, err := parseEnvelope(v)
	if err != nil {
		return nil, err
	}

	expect := DeriveKeyID(key)
	keyID := env.keyID
	if keyID == "" {
		keyID = expect
	} else if keyID != expect {
		return nil, fmt.Errorf("信封 keyId(%s) 与密钥(%s) 不匹配", keyID, expect)
	}

	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("构造 AES 失败: %w", err)
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, fmt.Errorf("构造 GCM 失败: %w", err)
	}

	// Go 的 GCM 期望 ciphertext||tag 拼接
	sealed := make([]byte, 0, len(env.ciphertext)+len(env.authTag))
	sealed = append(sealed, env.ciphertext...)
	sealed = append(sealed, env.authTag...)

	plain, err := gcm.Open(nil, env.nonce, sealed, buildAAD(keyID, env.suite, f))
	if err != nil {
		return nil, fmt.Errorf("GCM 解密失败（密钥或 AAD 不匹配）: %w", err)
	}
	return plain, nil
}

// DecryptField 解密 .info 里的一个字段；第二个返回值表示结果是否可信。
// 字段本来可能是明文（老版本客户端），这种情况原样返回。
func DecryptField(key []byte, v any) (string, bool) {
	if v == nil {
		return "", true
	}
	if !IsEnvelope(v) {
		if s, ok := v.(string); ok {
			return s, true
		}
		return "", false
	}
	plain, err := OpenEnvelope(key, v, frameField)
	if err != nil {
		return "", false
	}
	return string(plain), true
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%v", t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}