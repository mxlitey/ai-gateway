package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 本机桌面端登录态的导入。
//
// 链路：keyblob 拿判据 → 扫运行的进程内存找回 protector key → 自检 → 解 .info 字段。
// 桌面端自 2026-09 起把 token 换成 $wbEncrypted 信封，密钥不落盘，只能从进程内存找回。

// ---------- keyblob（~/.workbuddy/keyblob 或 ~/.workbuddy-ai/keyblob） ----------

type keySlot struct {
	Type           string `json:"type"`
	ProtectorKeyID string `json:"protectorKeyId"`
	Wrapped        any    `json:"wrapped"`
}

type keyBlob struct {
	KeyID   string    `json:"keyId"`
	Version int       `json:"version"`
	Slots   []keySlot `json:"slots"`
	Path    string    `json:"-"`
}

func configRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".workbuddy"),
		filepath.Join(home, ".workbuddy-ai"),
	}
}

func findKeyBlob() (*keyBlob, error) {
	for _, root := range configRoots() {
		p := filepath.Join(root, "keyblob")
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var b keyBlob
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", p, err)
		}
		b.Path = p
		return &b, nil
	}
	return nil, fmt.Errorf("没有找到 keyblob：桌面端没安装或从未登录过")
}

// ProtectorKeyID 返回 static-v1 槽的 protectorKeyId，用作内存扫描的判据。
func (b *keyBlob) ProtectorKeyID() (string, error) {
	for _, s := range b.Slots {
		if s.Type == "static-v1" && strings.TrimSpace(s.ProtectorKeyID) != "" {
			return strings.ToLower(s.ProtectorKeyID), nil
		}
	}
	return "", fmt.Errorf("keyblob 里没有 static-v1 槽：%s", b.Path)
}

// Unwrap 用 protector key 解出主密钥并自检；扫内存找到的候选必须过这一关才采信。
func (b *keyBlob) Unwrap(key []byte) ([]byte, error) {
	for _, s := range b.Slots {
		if s.Type != "static-v1" || s.Wrapped == nil {
			continue
		}
		if strings.ToLower(s.ProtectorKeyID) != DeriveKeyID(key) {
			return nil, fmt.Errorf("密钥与 keyblob 的 protectorKeyId 不匹配")
		}
		master, err := OpenEnvelope(key, s.Wrapped, frameFile)
		if err != nil {
			return nil, err
		}
		if len(master) != 32 || !strings.EqualFold(DeriveKeyID(master), b.KeyID) {
			return nil, fmt.Errorf("keyblob 解出的主密钥未通过校验")
		}
		return master, nil
	}
	return nil, fmt.Errorf("keyblob 里没有 static-v1 槽")
}

// ---------- .info 登录态文件 ----------

type desktopInfoFile struct {
	Site string
	Path string
}

func desktopInfoFiles() []desktopInfoFile {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return nil
	}
	dir := filepath.Join(base, "CodeBuddyExtension", "Data", "Public", "auth")
	return []desktopInfoFile{
		{Site: "cn", Path: filepath.Join(dir, "workbuddy-desktop.info")},
		{Site: "intl", Path: filepath.Join(dir, "workbuddy-desktop-ai.info")},
	}
}

// ImportFromDesktop 导入本机桌面端已登录的账号。
func ImportFromDesktop() ([]Account, []string, error) {
	blob, err := findKeyBlob()
	if err != nil {
		return nil, nil, err
	}
	keyID, err := blob.ProtectorKeyID()
	if err != nil {
		return nil, nil, err
	}

	key, err := findProtectorKey(keyID)
	if err != nil {
		return nil, nil, err
	}
	if _, err := blob.Unwrap(key); err != nil {
		return nil, nil, fmt.Errorf("找回的密钥未通过 keyblob 校验: %w", err)
	}

	var accounts []Account
	var notes []string
	for _, f := range desktopInfoFiles() {
		if _, err := os.Stat(f.Path); err != nil {
			continue
		}
		acc, err := readDesktopInfo(f, key)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s 版：%v", siteLabel(f.Site), err))
			continue
		}
		accounts = append(accounts, *acc)
	}
	if len(accounts) == 0 {
		if len(notes) == 0 {
			notes = append(notes, "本机没有找到桌面端登录态文件")
		}
		return nil, notes, fmt.Errorf("没有导入到账号：%s", strings.Join(notes, "；"))
	}
	return accounts, notes, nil
}

func readDesktopInfo(f desktopInfoFile, key []byte) (*Account, error) {
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析登录态失败: %w", err)
	}
	auth, _ := doc["auth"].(map[string]any)
	account, _ := doc["account"].(map[string]any)
	if auth == nil || account == nil {
		return nil, fmt.Errorf("登录态缺少 auth/account 段")
	}

	token, ok := DecryptField(key, auth["accessToken"])
	if !ok || token == "" {
		return nil, fmt.Errorf("无法解密 accessToken")
	}
	uid := asString(account["uid"])
	if uid == "" {
		return nil, fmt.Errorf("登录态缺少 uid")
	}

	name, _ := DecryptField(key, account["nickname"])
	if name == "" {
		name = shortUID(uid)
	}
	refresh, _ := DecryptField(key, auth["refreshToken"])

	// 区域以 token 的 issuer 为准（同一台机器可能切区登录过），文件名只作兜底
	site := realmFromToken(token, asString(auth["domain"]))
	if site == "" {
		site = f.Site
	}

	acc := &Account{
		ID:               uid,
		Name:             name,
		Site:             site,
		UID:              uid,
		AccessToken:      token,
		RefreshToken:     refresh,
		TokenExpiresAt:   normalizeEpochMillis(auth["expiresAt"]),
		RefreshExpiresAt: normalizeEpochMillis(auth["refreshExpiresAt"]),
		Domain:           asString(auth["domain"]),
		EnterpriseID:     asString(account["enterpriseId"]),
		Source:           "desktop",
		AddedAt:          time.Now().UnixMilli(),
	}
	if acc.TokenExpiresAt == 0 {
		acc.TokenExpiresAt = jwtExpMillis(token)
	}
	if acc.Domain == "" {
		acc.Domain = defaultDomain(site)
	}
	if acc.TokenExpiresAt > 0 && acc.TokenExpiresAt < time.Now().UnixMilli() {
		acc.LastError = "登录态已过期，请打开一次桌面端让它自动刷新"
	}
	return acc, nil
}

func shortUID(uid string) string {
	if len(uid) > 8 {
		return uid[:8]
	}
	return uid
}

func siteLabel(site string) string {
	if site == "intl" {
		return "国际"
	}
	return "国内"
}

func defaultDomain(site string) string {
	if site == "intl" {
		return "www.workbuddy.ai"
	}
	return "www.codebuddy.cn"
}