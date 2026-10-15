package main

import (
	"fmt"
	"strings"
)

// 手工导入（POST /console/accounts）：token 兼容两种形态 ——
//
//  1. TRAE 登录回调链接：走 ExchangeToken（轮换 refreshToken）+ GetUserInfo 补 uid/昵称；
//  2. 完整凭证 JSON：嵌套形 {"auth":{...},"account":{...}} 或扁平形 {"accessToken":...,"uid":...}，
//     直接校验落盘，不调 ExchangeToken。
//
// 两种都失败时返回中文错误；成功后以 uid 为准 upsert（已存在则更新，不存在则新增）。
func (p *Plugin) importCredential(token, name, uidHint string) (Account, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Account{}, false, fmt.Errorf("token 为空")
	}

	var a Auth
	if strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://") {
		info, err := ParseCallback(token)
		if err != nil {
			return Account{}, false, err
		}
		a = Auth{
			AccessToken:  info.AccessToken,
			RefreshToken: info.RefreshToken,
			UID:          info.UID,
			Nickname:     info.Nickname,
			EnterpriseID: info.EnterpriseID,
			ExpiresAt:    info.ExpiresAt,
			Domain:       "trae.cn",
			ApiHost:      p.eps.OAuthHost,
			MachineID:    randomHex(16),
			// DeviceID 故意留空：由下方 ensureDeviceID 补 16 位纯数字签到 deviceId
		}
		if strings.TrimSpace(a.RefreshToken) != "" {
			acc := authToAccount(a)
			if err := p.refreshToken(&acc); err != nil {
				return Account{}, false, fmt.Errorf("换取 access token 失败：%s", asUpstreamError(err).Msg)
			}
			a = acc.toAuth()
		}
		if uid, nick, ent, err := p.getUserInfo(&Account{AccessToken: a.AccessToken, ApiHost: a.ApiHost}); err == nil {
			if strings.TrimSpace(uid) != "" {
				a.UID = uid
			}
			if strings.TrimSpace(nick) != "" {
				a.Nickname = nick
			}
			if strings.TrimSpace(ent) != "" {
				a.EnterpriseID = ent
			}
		}
		if strings.TrimSpace(a.UID) == "" {
			return Account{}, false, fmt.Errorf("无法从回调链接确定用户 id，请确认链接完整")
		}
		if strings.TrimSpace(a.AccessToken) == "" {
			return Account{}, false, fmt.Errorf("回调链接换取 access token 失败")
		}
	} else {
		parsed, err := parseAuth([]byte(token))
		if err != nil {
			return Account{}, false, fmt.Errorf("token 既不是回调链接也不是合法凭证 JSON")
		}
		a = *parsed
		if strings.TrimSpace(a.UID) == "" {
			a.UID = strings.TrimSpace(uidHint)
		}
		if strings.TrimSpace(a.UID) == "" {
			a.UID = uidFromToken(a.AccessToken)
		}
		if strings.TrimSpace(a.AccessToken) == "" {
			return Account{}, false, fmt.Errorf("凭证 JSON 缺少 accessToken")
		}
		if strings.TrimSpace(a.UID) == "" {
			return Account{}, false, fmt.Errorf("凭证 JSON 缺少 uid，且无法从 accessToken 解析")
		}
		if strings.TrimSpace(a.Domain) == "" {
			a.Domain = "trae.cn"
		}
		if strings.TrimSpace(a.ApiHost) == "" {
			a.ApiHost = p.eps.OAuthHost
		}
	}

	if strings.TrimSpace(name) != "" {
		a.Nickname = strings.TrimSpace(name)
	}
	if err := ensureDeviceID(&a); err != nil {
		return Account{}, false, fmt.Errorf("生成 deviceId 失败：%v", err)
	}
	if strings.TrimSpace(a.Domain) == "" {
		a.Domain = "trae.cn"
	}
	if strings.TrimSpace(a.ApiHost) == "" {
		a.ApiHost = p.eps.OAuthHost
	}

	saved, created := p.store.upsert(a)
	return saved, created, nil
}

func authToAccount(a Auth) Account {
	return Account{
		UID: a.UID, Nickname: a.Nickname, EnterpriseID: a.EnterpriseID,
		AccessToken: a.AccessToken, RefreshToken: a.RefreshToken, ExpiresAt: a.ExpiresAt,
		Domain: a.Domain, ApiHost: a.ApiHost, MachineID: a.MachineID, DeviceID: a.DeviceID,
		Enabled: true,
	}
}

// ensureDeviceID 仅在 DeviceID 为空时补一个 16 位数字签到 deviceId。
func ensureDeviceID(a *Auth) error {
	if strings.TrimSpace(a.DeviceID) != "" {
		return nil
	}
	id, err := newCheckinDeviceID()
	if err != nil {
		return err
	}
	a.DeviceID = id
	return nil
}