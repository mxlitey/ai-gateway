package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Auth 是单账号的凭证，落盘在 <data>/auths/trae-<uid>.json（UTF-8、0600、tmp+rename 原子写）。
//
// 磁盘形态与参考项目保持一致（嵌套形 {"account":{...},"auth":{...}}），
// 解析时同时兼容扁平形（{"accessToken":...,"uid":...}），这样参考项目导出的凭证可直接放进 auths/ 使用。
type Auth struct {
	UID          string `json:"uid"`
	Nickname     string `json:"nickname,omitempty"`
	EnterpriseID string `json:"enterpriseId,omitempty"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    int64  `json:"expiresAt"` // Unix 秒（0 表示不过期）
	Domain       string `json:"domain,omitempty"`
	ApiHost      string `json:"apiHost,omitempty"`
	MachineID    string `json:"machineId,omitempty"`
	DeviceID     string `json:"deviceId,omitempty"`
}

// Account 是「凭证 + 状态」的合并视图（对外控制台与内部调度都用它）。
type Account struct {
	UID          string
	Nickname     string
	EnterpriseID string
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64 // Unix 秒
	Domain       string
	ApiHost      string
	MachineID    string
	DeviceID     string

	Enabled        bool
	HardDisabled   bool // 登录态失效等硬禁用
	Credits        float64
	CreditsAt      int64
	CheckedInToday bool
	StreakDays     int
	LastError      string
}

// name 返回展示名：昵称为空时回退 uid。
func (a Account) name() string { return firstNonEmpty(strings.TrimSpace(a.Nickname), a.UID) }

// usable 报告账号是否可被选号使用（软开关开启且未被硬禁用）。
func (a Account) usable() bool { return a.Enabled && !a.HardDisabled }

// tokenAlive 报告 accessToken 是否仍在有效期内。
func (a Account) tokenAlive() bool {
	return strings.TrimSpace(a.AccessToken) != "" &&
		(a.ExpiresAt == 0 || a.ExpiresAt > time.Now().Unix())
}

func (a Account) toAuth() Auth {
	return Auth{
		UID: a.UID, Nickname: a.Nickname, EnterpriseID: a.EnterpriseID,
		AccessToken: a.AccessToken, RefreshToken: a.RefreshToken, ExpiresAt: a.ExpiresAt,
		Domain: a.Domain, ApiHost: a.ApiHost, MachineID: a.MachineID, DeviceID: a.DeviceID,
	}
}

// accountState 是 state.json 里单个账号的可持久化状态（不含任何凭据）。
type accountState struct {
	Enabled        bool    `json:"enabled"`
	HardDisabled   bool    `json:"hard_disabled,omitempty"`
	Credits        float64 `json:"credits"`
	CreditsAt      int64   `json:"credits_at,omitempty"`
	CheckedInToday bool    `json:"checked_in_today"`
	StreakDays     int     `json:"streak_days,omitempty"`
	LastError      string  `json:"last_error,omitempty"`
}

// stateFile 是 state.json 的顶层结构；schema 用于数据目录的自行迁移。
type stateFile struct {
	Schema   int                      `json:"schema"`
	Accounts map[string]*accountState `json:"accounts"`
}

const stateSchema = 1

// accountStore 统一管理 auths/ 下的凭证文件与 state.json 里的账号状态。
type accountStore struct {
	mu        sync.RWMutex
	dataDir   string
	authDir   string
	statePath string
	order     []string
	auths     map[string]*Auth
	states    map[string]*accountState
}

func newAccountStore(dataDir string) (*accountStore, error) {
	authDir := filepath.Join(dataDir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	s := &accountStore{
		dataDir:   dataDir,
		authDir:   authDir,
		statePath: filepath.Join(dataDir, "state.json"),
		auths:     map[string]*Auth{},
		states:    map[string]*accountState{},
	}
	s.loadState()
	s.loadAuths()
	return s, nil
}

func (s *accountStore) loadState() {
	raw, err := os.ReadFile(s.statePath)
	if err != nil {
		return
	}
	var doc stateFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	for uid, st := range doc.Accounts {
		if st == nil {
			continue
		}
		s.states[uid] = st
	}
	// schema != 当前版本时保留数据不动、按当前结构继续读（预留迁移点）。
	_ = doc.Schema
}

// loadAuths 扫描 auths/trae-*.json；解析失败的文件静默跳过。
func (s *accountStore) loadAuths() {
	files, _ := filepath.Glob(filepath.Join(s.authDir, "trae-*.json"))
	uids := make([]string, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		a, err := parseAuth(raw)
		if err != nil || strings.TrimSpace(a.AccessToken) == "" {
			continue
		}
		if a.UID == "" {
			a.UID = uidFromPath(f)
		}
		if a.UID == "" {
			continue
		}
		s.auths[a.UID] = a
		if _, ok := s.states[a.UID]; !ok {
			s.states[a.UID] = &accountState{Enabled: true}
		}
		uids = append(uids, a.UID)
	}
	sort.Strings(uids)
	s.order = uids
}

func uidFromPath(p string) string {
	base := strings.TrimSuffix(filepath.Base(p), ".json")
	return strings.TrimPrefix(base, "trae-")
}

// ---------- 读 ----------

func (s *accountStore) list() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, 0, len(s.order))
	for _, uid := range s.order {
		if a, ok := s.snapshotLocked(uid); ok {
			out = append(out, a)
		}
	}
	return out
}

func (s *accountStore) get(id string) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked(id)
}

func (s *accountStore) snapshotLocked(uid string) (Account, bool) {
	a, ok := s.auths[uid]
	if !ok {
		return Account{}, false
	}
	acc := Account{
		UID: a.UID, Nickname: a.Nickname, EnterpriseID: a.EnterpriseID,
		AccessToken: a.AccessToken, RefreshToken: a.RefreshToken, ExpiresAt: a.ExpiresAt,
		Domain: a.Domain, ApiHost: a.ApiHost, MachineID: a.MachineID, DeviceID: a.DeviceID,
		Enabled: true,
	}
	if st, ok := s.states[uid]; ok {
		acc.Enabled = st.Enabled
		acc.HardDisabled = st.HardDisabled
		acc.Credits = st.Credits
		acc.CreditsAt = st.CreditsAt
		acc.CheckedInToday = st.CheckedInToday
		acc.StreakDays = st.StreakDays
		acc.LastError = st.LastError
	}
	return acc, true
}

// ---------- 写 ----------

// upsert 以 uid 为准合并账号：凭证整体替换，状态字段（额度/签到/开关）保留既有值。
func (s *accountStore) upsert(a Auth) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.auths[a.UID]
	stored := a
	s.auths[a.UID] = &stored
	if _, ok := s.states[a.UID]; !ok {
		s.states[a.UID] = &accountState{Enabled: true}
	}
	if !containsUID(s.order, a.UID) {
		s.order = append(s.order, a.UID)
	}
	_ = s.saveAuthLocked(a.UID)
	_ = s.saveStateLocked()
	acc, _ := s.snapshotLocked(a.UID)
	return acc, !existed
}

// saveAuth 写回更新后的凭证（token 刷新后调用）。
func (s *accountStore) saveAuth(a Auth) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.auths[a.UID]; !ok {
		return fmt.Errorf("账号 %s 不存在", a.UID)
	}
	stored := a
	s.auths[a.UID] = &stored
	return s.saveAuthLocked(a.UID)
}

func (s *accountStore) remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.auths[id]; !ok {
		return false
	}
	delete(s.auths, id)
	delete(s.states, id)
	for i, uid := range s.order {
		if uid == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	_ = os.Remove(filepath.Join(s.authDir, "trae-"+id+".json"))
	_ = s.saveStateLocked()
	return true
}

// updateState 在锁内改一个账号的状态并落盘，返回改完的快照。
func (s *accountStore) updateState(id string, fn func(*accountState)) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.auths[id]; !ok {
		return Account{}, false
	}
	st, ok := s.states[id]
	if !ok {
		st = &accountState{Enabled: true}
		s.states[id] = st
	}
	fn(st)
	_ = s.saveStateLocked()
	acc, _ := s.snapshotLocked(id)
	return acc, true
}

// ---------- 落盘 ----------

func (s *accountStore) saveAuthLocked(uid string) error {
	a := s.auths[uid]
	if a == nil {
		return fmt.Errorf("账号 %s 不存在", uid)
	}
	raw, err := marshalAuthNested(a)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.authDir, "trae-"+uid+".json"), raw)
}

func (s *accountStore) saveStateLocked() error {
	doc := stateFile{Schema: stateSchema, Accounts: s.states}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.statePath, raw)
}

// atomicWrite 以 tmp+rename 原子写文件（0600），避免中断损坏已有内容。
func atomicWrite(path string, raw []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------- 凭证文件序列化 ----------

func marshalAuthNested(a *Auth) ([]byte, error) {
	doc := map[string]any{
		"auth": map[string]any{
			"accessToken":  a.AccessToken,
			"refreshToken": a.RefreshToken,
			"expiresAt":    a.ExpiresAt,
			"domain":       a.Domain,
			"apiHost":      a.ApiHost,
			"machineId":    a.MachineID,
			"deviceId":     a.DeviceID,
		},
		"account": map[string]any{
			"uid":          a.UID,
			"enterpriseId": a.EnterpriseID,
			"nickname":     a.Nickname,
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}

// parseAuth 兼容嵌套形与扁平形两种凭证 JSON。
func parseAuth(raw []byte) (*Auth, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("空凭证")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("凭证不是合法 JSON")
	}
	if _, nested := probe["auth"]; nested {
		var n struct {
			Auth struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpiresAt    int64  `json:"expiresAt"`
				Domain       string `json:"domain"`
				ApiHost      string `json:"apiHost"`
				MachineID    string `json:"machineId"`
				DeviceID     string `json:"deviceId"`
			} `json:"auth"`
			Account struct {
				UID          string `json:"uid"`
				EnterpriseID string `json:"enterpriseId"`
				Nickname     string `json:"nickname"`
			} `json:"account"`
		}
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, fmt.Errorf("解析嵌套凭证失败")
		}
		return &Auth{
			UID: n.Account.UID, Nickname: n.Account.Nickname, EnterpriseID: n.Account.EnterpriseID,
			AccessToken: n.Auth.AccessToken, RefreshToken: n.Auth.RefreshToken, ExpiresAt: n.Auth.ExpiresAt,
			Domain: n.Auth.Domain, ApiHost: n.Auth.ApiHost, MachineID: n.Auth.MachineID, DeviceID: n.Auth.DeviceID,
		}, nil
	}
	var f Auth
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析凭证失败")
	}
	return &f, nil
}

// ---------- 小工具 ----------

func containsUID(list []string, uid string) bool {
	for _, v := range list {
		if v == uid {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// uidFromToken 尽力从 accessToken（Cloud-IDE-JWT）解析用户 id，供手工导入兜底。
func uidFromToken(tok string) string {
	parts := strings.Split(strings.TrimSpace(tok), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		if payload, err = base64.URLEncoding.DecodeString(parts[1]); err != nil {
			return ""
		}
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	for _, k := range []string{"sub", "uid", "user_id", "userId", "UserID"} {
		if v, ok := claims[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
		if v, ok := claims[k].(float64); ok {
			return fmt.Sprintf("%.0f", v)
		}
	}
	return ""
}