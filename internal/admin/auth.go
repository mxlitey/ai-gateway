// Package admin 提供管理端能力：管理员密码、会话签发与鉴权。
package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/store"
)

const (
	pbkdf2Iter    = 210000
	pbkdf2KeyLen  = 32
	saltLen       = 16
	sessionTTL    = 24 * time.Hour
	passwordMinLen = 8
)

// Service 管理端服务。
type Service struct {
	store *store.Store
}

// New 创建管理端服务。
func New(s *store.Store) *Service {
	return &Service{store: s}
}

// ---------- 首次初始化 ----------

// IsInitialized 是否已完成首次设置。
func (a *Service) IsInitialized() (bool, error) {
	v, ok, err := a.store.GetSetting(store.SettingInitialized)
	if err != nil {
		return false, err
	}
	return ok && v == "1", nil
}

// Setup 首次设置管理员密码（已初始化时返回错误）。
func (a *Service) Setup(password string) error {
	initialized, err := a.IsInitialized()
	if err != nil {
		return err
	}
	if initialized {
		return fmt.Errorf("已初始化")
	}
	return a.setPassword(password)
}

// SetPassword 直接设置/修改管理员密码（不做初始化校验）。
func (a *Service) SetPassword(password string) error {
	return a.setPassword(password)
}

func (a *Service) setPassword(password string) error {
	if len(password) < passwordMinLen {
		return fmt.Errorf("密码长度至少 %d 位", passwordMinLen)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	if err := a.store.SetSetting(store.SettingAdminPasswordHash, hash); err != nil {
		return err
	}
	// 首次设置时生成会话签名密钥
	if _, ok, err := a.store.GetSetting(store.SettingAdminSessionKey); err != nil {
		return err
	} else if !ok {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return err
		}
		if err := a.store.SetSetting(store.SettingAdminSessionKey, hex.EncodeToString(buf)); err != nil {
			return err
		}
	}
	return a.store.SetSetting(store.SettingInitialized, "1")
}

// ---------- 登录 ----------

// Login 校验管理员密码，成功时返回会话 Token。
//
// 仅接受管理员密码；客户端 API Key 不得用于登录管理面板。
func (a *Service) Login(credential string) (string, bool, error) {
	encoded, ok, err := a.store.GetSetting(store.SettingAdminPasswordHash)
	if err != nil {
		return "", false, err
	}
	if !ok || !VerifyPassword(credential, encoded) {
		return "", false, nil
	}
	secret, err := a.sessionSecret()
	if err != nil {
		return "", false, err
	}
	token, err := CreateToken(secret, sessionTTL)
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

// RequireAuth 校验管理端请求：仅接受管理员会话 Token。
func (a *Service) RequireAuth(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	bearer := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if bearer == "" {
		return false
	}

	secret, err := a.sessionSecret()
	if err != nil {
		return false
	}
	return VerifyToken(bearer, secret)
}

func (a *Service) sessionSecret() ([]byte, error) {
	v, ok, err := a.store.GetSetting(store.SettingAdminSessionKey)
	if err != nil {
		return nil, err
	}
	if !ok || v == "" {
		return nil, fmt.Errorf("会话密钥未初始化")
	}
	return hex.DecodeString(v)
}

// ---------- 会话 Token ----------

// CreateToken 签发会话 Token：base64url(payload).base64url(hmac)。
func CreateToken(secret []byte, ttl time.Duration) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"exp":  time.Now().Add(ttl).UnixMilli(),
		"role": "admin",
	})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + sign(encoded, secret), nil
}

// VerifyToken 校验会话 Token 的签名与有效期。
func VerifyToken(token string, secret []byte) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	expected := sign(parts[0], secret)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) != 1 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var p struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	return time.Now().UnixMilli() < p.Exp
}

func sign(payload string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ---------- 密码哈希（PBKDF2-HMAC-SHA256） ----------

// HashPassword 生成 pbkdf2$sha256$<iter>$<salt_b64>$<hash_b64> 形式的摘要。
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk := pbkdf2SHA256([]byte(password), salt, pbkdf2Iter, pbkdf2KeyLen)
	return fmt.Sprintf("pbkdf2$sha256$%d$%s$%s",
		pbkdf2Iter,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk),
	), nil
}

// VerifyPassword 校验密码是否匹配摘要（常量时间比较）。
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[2])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// pbkdf2SHA256 是 PBKDF2-HMAC-SHA256 的最小实现（避免引入 x/crypto 依赖）。
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen

	var buf [4]byte
	dk := make([]byte, 0, numBlocks*hashLen)
	u := make([]byte, hashLen)

	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		prf.Write(buf[:4])

		dk = prf.Sum(dk)
		t := dk[len(dk)-hashLen:]
		copy(u, t)

		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = u[:0]
			u = prf.Sum(u)
			for x := range u {
				t[x] ^= u[x]
			}
		}
	}
	return dk[:keyLen]
}