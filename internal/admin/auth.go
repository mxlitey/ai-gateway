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
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/store"
)

const (
	pbkdf2Iter     = 210000
	pbkdf2KeyLen   = 32
	saltLen        = 16
	passwordMinLen = 8

	// sessionTTL 普通登录的会话有效期。
	sessionTTL = 24 * time.Hour
	// rememberedTTL 「记住此设备」时的会话有效期（30 天）。
	rememberedTTL = 30 * 24 * time.Hour

	// loginFreeAttempts 允许的连续失败次数，未超过时不退避（正常人可能手滑一两次）。
	loginFreeAttempts = 3
	// loginBackoffBase 超过免费次数后的首次退避时长，此后每次翻倍。
	loginBackoffBase = time.Second
	// loginBackoffMax 退避时长上限。
	loginBackoffMax = time.Minute
)

// ErrLoginThrottled 表示登录因连续失败被退避限制，需等待后重试。
var ErrLoginThrottled = errors.New("登录失败次数过多，请稍后再试")

// Service 管理端服务。
type Service struct {
	store *store.Store
	// loginMu 串行化登录流程：失败计数读-改-写必须原子，否则并发爆破会绕过退避。
	loginMu sync.Mutex
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
	// 每次设置密码都重新生成会话签名密钥：改密后旧 Token 立即失效。
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	if err := a.store.SetSetting(store.SettingAdminSessionKey, hex.EncodeToString(buf)); err != nil {
		return err
	}
	// 改密成功说明是合法管理员操作，顺带清空失败计数与锁定
	a.resetLoginFailures()
	return a.store.SetSetting(store.SettingInitialized, "1")
}

// ---------- 登录 ----------

// Login 校验管理员密码，成功时返回会话 Token。
//
// 仅接受管理员密码；客户端 API Key 不得用于登录管理面板。
// remember 为 true 时会话有效期延长到 30 天（面板上的「记住此设备」），否则为 24 小时。
//
// 连续失败会触发指数退避：计数与解锁时间持久化在 settings 中，跨重启生效。
// 由于网关通常挂在 CDN 后面，回源 IP 会变化，这里刻意不使用来源 IP / UA 作为维度，
// 而是对「整个管理端」做全局限制。
func (a *Service) Login(credential string, remember bool) (string, bool, error) {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()

	if remain := a.loginBlockRemaining(); remain > 0 {
		return "", false, fmt.Errorf("%w（还需等待 %d 秒）", ErrLoginThrottled, int(remain.Seconds()+0.5))
	}

	encoded, ok, err := a.store.GetSetting(store.SettingAdminPasswordHash)
	if err != nil {
		return "", false, err
	}
	if !ok || !VerifyPassword(credential, encoded) {
		a.recordLoginFailure()
		return "", false, nil
	}

	// 登录成功：清空失败计数，避免一次误输影响后续登录
	a.resetLoginFailuresLocked()

	secret, err := a.sessionSecret()
	if err != nil {
		return "", false, err
	}
	ttl := sessionTTL
	if remember {
		ttl = rememberedTTL
	}
	token, err := CreateToken(secret, ttl)
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

// loginBlockRemaining 返回当前锁定剩余时长，0 表示可以尝试登录。
func (a *Service) loginBlockRemaining() time.Duration {
	v, ok, err := a.store.GetSetting(store.SettingAdminLoginBlockedUntil)
	if err != nil || !ok || v == "" {
		return 0
	}
	until, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	remain := time.Until(time.UnixMilli(until))
	if remain <= 0 {
		return 0
	}
	return remain
}

// recordLoginFailure 累加失败次数，超过免费次数后写入指数退避的解锁时间。
// 调用方必须持有 loginMu。
func (a *Service) recordLoginFailure() {
	failures := 0
	if v, ok, err := a.store.GetSetting(store.SettingAdminLoginFailures); err == nil && ok {
		if n, perr := strconv.Atoi(v); perr == nil {
			failures = n
		}
	}
	failures++
	_ = a.store.SetSetting(store.SettingAdminLoginFailures, strconv.Itoa(failures))

	if failures <= loginFreeAttempts {
		return
	}
	delay := loginBackoffBase
	// 第 4 次失败等 1s，第 5 次 2s，第 6 次 4s …… 封顶 loginBackoffMax
	for i := loginFreeAttempts + 1; i < failures && delay < loginBackoffMax; i++ {
		delay *= 2
	}
	if delay > loginBackoffMax {
		delay = loginBackoffMax
	}
	until := time.Now().Add(delay).UnixMilli()
	_ = a.store.SetSetting(store.SettingAdminLoginBlockedUntil, strconv.FormatInt(until, 10))
}

// resetLoginFailures 清空失败计数与锁定（自行加锁）。
func (a *Service) resetLoginFailures() {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	a.resetLoginFailuresLocked()
}

// resetLoginFailuresLocked 清空失败计数与锁定。调用方必须持有 loginMu。
func (a *Service) resetLoginFailuresLocked() {
	_ = a.store.SetSetting(store.SettingAdminLoginFailures, "0")
	_ = a.store.SetSetting(store.SettingAdminLoginBlockedUntil, "0")
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
