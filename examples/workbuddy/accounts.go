package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Account 是插件管理的账号。凭据是明文保存在插件私有数据目录里的（仅本机、0600），
// 因为上游要求的是原始 token，而插件自身不做加密存储。
type Account struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Site             string  `json:"site"`
	UID              string  `json:"uid"`
	Domain           string  `json:"domain"`
	AccessToken      string  `json:"access_token"`
	RefreshToken     string  `json:"refresh_token,omitempty"`
	TokenExpiresAt   int64   `json:"expires_at"`
	RefreshExpiresAt int64   `json:"refresh_expires_at,omitempty"`
	Plan             string  `json:"plan"`
	Credits          float64 `json:"credits"`
	CreditsAt        int64   `json:"credits_updated_at"`
	CheckedInToday   bool    `json:"checked_in_today"`
	StreakDays       int     `json:"streak_days"`
	TodayCredit      float64 `json:"today_credit"`
	LastError        string  `json:"last_error"`
	Source           string  `json:"source,omitempty"`
	AddedAt          int64   `json:"added_at"`
	Enabled          bool    `json:"enabled"`

	// 企业号与设备令牌（个人号留空）
	EnterpriseID string `json:"enterprise_id,omitempty"`
	DeviceToken  string `json:"device_token,omitempty"`
}

// tokenAlive 判断 accessToken 是否还在有效期内。
func (a *Account) tokenAlive() bool {
	return a.AccessToken != "" && (a.TokenExpiresAt == 0 || a.TokenExpiresAt > time.Now().UnixMilli())
}

type accountStore struct {
	mu       sync.Mutex
	path     string
	accounts []Account
}

func newAccountStore(dataDir string) (*accountStore, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	s := &accountStore{path: filepath.Join(dataDir, "accounts.json")}
	if raw, err := os.ReadFile(s.path); err == nil {
		var doc struct {
			Accounts []Account `json:"accounts"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", s.path, err)
		}
		s.accounts = doc.Accounts
	}
	return s, nil
}

func (s *accountStore) list() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Account, len(s.accounts))
	copy(out, s.accounts)
	return out
}

func (s *accountStore) get(id string) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.accounts {
		if a.ID == id {
			return a, true
		}
	}
	return Account{}, false
}

// upsert 按 id 合并：已存在则保留面板上的统计值、只刷新凭据字段。
func (s *accountStore) upsert(acc Account) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.accounts {
		if s.accounts[i].ID != acc.ID {
			continue
		}
		cur := s.accounts[i]
		acc.Credits = cur.Credits
		acc.CreditsAt = cur.CreditsAt
		acc.CheckedInToday = cur.CheckedInToday
		acc.StreakDays = cur.StreakDays
		acc.TodayCredit = cur.TodayCredit
		acc.Plan = cur.Plan
		acc.Enabled = cur.Enabled
		acc.AddedAt = cur.AddedAt
		s.accounts[i] = acc
		_ = s.saveLocked()
		return acc, false
	}

	acc.Enabled = true
	s.accounts = append(s.accounts, acc)
	_ = s.saveLocked()
	return acc, true
}

func (s *accountStore) remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if s.accounts[i].ID == id {
			s.accounts = append(s.accounts[:i], s.accounts[i+1:]...)
			_ = s.saveLocked()
			return true
		}
	}
	return false
}

// update 在锁内改一个账号并落盘，返回改完的结果。
func (s *accountStore) update(id string, fn func(*Account)) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if s.accounts[i].ID != id {
			continue
		}
		fn(&s.accounts[i])
		_ = s.saveLocked()
		return s.accounts[i], true
	}
	return Account{}, false
}

func (s *accountStore) saveLocked() error {
	doc := struct {
		Accounts []Account `json:"accounts"`
	}{Accounts: s.accounts}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}