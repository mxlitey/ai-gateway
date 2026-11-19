package httpapi

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
	"strings"
	"sync"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/store"
)

// 上传令牌（签名路径）。
//
// 形态：base64url(payload) + "." + base64url(HMAC-SHA256(payload))
//
//	payload = {"exp":<unix>,"sid":"<16 位十六进制>","mb":<单张上限,可选>}
//
// 令牌直接进路径，因此不依赖任何请求头：
//
//	POST   /v1/uploads/{token}          上传
//	DELETE /v1/uploads/{token}/{name}   删除该令牌上传的单个文件
//	DELETE /v1/uploads/{token}          删除该令牌上传的全部文件并吊销令牌
//
// 设计取舍（见 Release Notes）：
//   - 不绑 API Key：校验完全无状态，只验签 + 查过期，不查库；
//   - 因此禁用 sk- 不会让已签发的令牌失效，令牌最长 24 小时后自然过期；
//   - 令牌在路径里会进入反向代理日志，故 TTL 默认很短，且只能操作自己上传的文件。
const (
	// uploadTokenPrefix 不是 URL 前缀，仅用于日志与错误信息里区分令牌类型。
	uploadTokenTTLMax = 24 * time.Hour
	// uploadTokenTTLDefault 签发接口的默认有效期。
	uploadTokenTTLDefault = 10 * time.Minute
	// sidLen 归属标识的十六进制长度（8 字节随机）。
	sidLen = 16
)

var (
	errUploadTokenInvalid = errors.New("上传令牌无效")
	errUploadTokenExpired = errors.New("上传令牌已过期或已被吊销")
	errUploadTokenRevoked = errors.New("上传令牌已被吊销")
)

// uploadTokenPayload 令牌载荷。字段短名是为了压短路径。
type uploadTokenPayload struct {
	Exp int64  `json:"exp"`
	SID string `json:"sid"`
	MaxMB int  `json:"mb,omitempty"`
}

// uploadToken 解析后的令牌。
type uploadToken struct {
	payload    uploadTokenPayload
	raw        string
}

// uploadTokenIssuer 负责签发与校验上传令牌，并维护吊销名单。
//
// 签名密钥存在 settings 里（首次使用时生成），吊销名单同样落库，
// 因此网关重启不会让已吊销的令牌复活。
type uploadTokenIssuer struct {
	store *store.Store

	mu     sync.Mutex
	secret []byte
	// revoked sid -> 过期时间（unix），只保留尚未到期的条目
	revoked map[string]int64
	loaded  bool
}

func newUploadTokenIssuer(st *store.Store) *uploadTokenIssuer {
	return &uploadTokenIssuer{store: st, revoked: map[string]int64{}}
}

// Mint 签发一个上传令牌。
//
// ttl 由调用方指定，超过 24 小时按 24 小时截断；maxMB <= 0 表示不额外收紧。
func (i *uploadTokenIssuer) Mint(ttl time.Duration, maxMB int) (token string, payload uploadTokenPayload, err error) {
	if ttl <= 0 {
		ttl = uploadTokenTTLDefault
	}
	if ttl > uploadTokenTTLMax {
		ttl = uploadTokenTTLMax
	}
	sid, err := randomSID()
	if err != nil {
		return "", uploadTokenPayload{}, err
	}
	payload = uploadTokenPayload{
		Exp:   time.Now().Add(ttl).Unix(),
		SID:   sid,
		MaxMB: maxMB,
	}
	secret, err := i.signingSecret()
	if err != nil {
		return "", uploadTokenPayload{}, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", uploadTokenPayload{}, err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + signBytes(body, secret), payload, nil
}

// Verify 校验令牌的签名与有效期，并检查是否已被吊销。
func (i *uploadTokenIssuer) Verify(token string) (uploadTokenPayload, error) {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return uploadTokenPayload{}, errUploadTokenInvalid
	}
	secret, err := i.signingSecret()
	if err != nil {
		return uploadTokenPayload{}, err
	}
	expected := signBytes(parts[0], secret)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) != 1 {
		return uploadTokenPayload{}, errUploadTokenInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return uploadTokenPayload{}, errUploadTokenInvalid
	}
	var p uploadTokenPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return uploadTokenPayload{}, errUploadTokenInvalid
	}
	if p.SID == "" || len(p.SID) != sidLen || p.Exp <= 0 {
		return uploadTokenPayload{}, errUploadTokenInvalid
	}
	if time.Now().Unix() >= p.Exp {
		return uploadTokenPayload{}, errUploadTokenExpired
	}
	if i.isRevoked(p.SID) {
		return uploadTokenPayload{}, errUploadTokenRevoked
	}
	return p, nil
}

// Revoke 把某个 sid 加入吊销名单，直到它原本的过期时间为止。
func (i *uploadTokenIssuer) Revoke(sid string, exp int64) {
	i.mu.Lock()
	defer i.mu.Unlock()
	now := time.Now().Unix()
	for s, e := range i.revoked {
		if e <= now {
			delete(i.revoked, s)
		}
	}
	if exp > now {
		i.revoked[sid] = exp
	}
	i.persistLocked()
}

func (i *uploadTokenIssuer) isRevoked(sid string) bool {
	if i.store == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.loaded {
		i.loadLocked()
	}
	exp, ok := i.revoked[sid]
	if !ok {
		return false
	}
	if exp <= time.Now().Unix() {
		delete(i.revoked, sid)
		i.persistLocked()
		return false
	}
	return true
}

// signingSecret 读取（或首次生成）签名密钥。
func (i *uploadTokenIssuer) signingSecret() ([]byte, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.secret != nil {
		return i.secret, nil
	}
	if i.store == nil {
		// 无存储时退化为进程内随机密钥：重启后旧令牌全部失效，符合安全预期
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		i.secret = buf
		return i.secret, nil
	}
	v, ok, err := i.store.GetSetting(store.SettingImageUploadSecret)
	if err != nil {
		return nil, fmt.Errorf("读取上传令牌密钥失败: %w", err)
	}
	if !ok || v == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		if err := i.store.SetSetting(store.SettingImageUploadSecret, hex.EncodeToString(buf)); err != nil {
			return nil, fmt.Errorf("保存上传令牌密钥失败: %w", err)
		}
		i.secret = buf
		return i.secret, nil
	}
	buf, err := hex.DecodeString(v)
	if err != nil || len(buf) == 0 {
		return nil, fmt.Errorf("上传令牌密钥格式非法")
	}
	i.secret = buf
	return i.secret, nil
}

func (i *uploadTokenIssuer) loadLocked() {
	i.loaded = true
	if i.store == nil {
		return
	}
	v, ok, err := i.store.GetSetting(store.SettingImageUploadRevoked)
	if err != nil || !ok || v == "" {
		return
	}
	var m map[string]int64
	if err := json.Unmarshal([]byte(v), &m); err != nil {
		return
	}
	now := time.Now().Unix()
	for sid, exp := range m {
		if exp > now {
			i.revoked[sid] = exp
		}
	}
}

func (i *uploadTokenIssuer) persistLocked() {
	if i.store == nil {
		return
	}
	if len(i.revoked) == 0 {
		_ = i.store.DeleteSetting(store.SettingImageUploadRevoked)
		return
	}
	raw, err := json.Marshal(i.revoked)
	if err != nil {
		return
	}
	_ = i.store.SetSetting(store.SettingImageUploadRevoked, string(raw))
}

func randomSID() (string, error) {
	buf := make([]byte, sidLen/2)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成令牌标识失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func signBytes(payload string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
