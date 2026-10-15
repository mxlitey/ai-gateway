package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
)

// Endpoints 是 TRAE 上游的全部主机与接口路径模板，均可在 exe 同级 config.json 里覆盖：
// 上游一改，只改配置、不动代码（写法与 workbuddy 的 Endpoints 一致）。
//
// 取值来源：AgentHub electron/backend/proxy/rules.cjs 的 headers.json.trae（复刻官方客户端指纹）。
type Endpoints struct {
	AgentHost   string `json:"agent_host"`   // 对话 / 模型表
	UgHost      string `json:"ug_host"`      // 签到 / 积分
	OAuthHost   string `json:"oauth_host"`   // ExchangeToken / GetUserInfo
	ConsoleHost string `json:"console_host"` // 网页登录授权页

	ClientID string `json:"client_id"`
	AppID    string `json:"app_id"`
	// UserAgent 是对话/模型请求的官方客户端指纹；旧值 "Trae/<IdeVersion>" 会被风控识别为非官方客户端。
	UserAgent string `json:"user_agent"`
	// AuthAppVersion 是登录授权页（x_app_version）与 GetUserInfo 用的版本号。
	// ⚠️ 与 IdeVersion 不是一套，别混用（AgentHub 明确警告）。
	AuthAppVersion string `json:"auth_app_version"`
	IdeVersion     string `json:"ide_version"`
	IdeVersionCode string `json:"ide_version_code"`
	DeviceBrand    string `json:"device_brand"`
	OSVersion      string `json:"os_version"`
	Function       string `json:"function"`
	// ChatMirror 是可选的对话镜像域**完整 URL**；留空则只用主域（官方对内外只有一个 agent 域）。
	ChatMirror string `json:"chat_mirror"`

	EpChat          string `json:"ep_chat"`
	EpModels        string `json:"ep_models"`
	EpExchange      string `json:"ep_exchange"`
	EpUserInfo      string `json:"ep_user_info"`
	EpCheckinStatus string `json:"ep_checkin_status"`
	EpCheckinClaim  string `json:"ep_checkin_claim"`
	EpEntUsage      string `json:"ep_ent_usage"` // 额度明细（面板「积分」列的数据源）
}

func defaultEndpoints() Endpoints {
	return Endpoints{
		AgentHost:   "https://trae-api-cn.mchost.guru",
		UgHost:      "https://api.trae.cn",
		OAuthHost:   "https://api.trae.com.cn",
		ConsoleHost: "https://www.trae.cn",

		ClientID:       "en1oxy7wnw8j9n",
		AppID:          "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8",
		UserAgent:      "TraeClient/TTNet",
		AuthAppVersion: "3.5.66",
		IdeVersion:     "0.1.50",
		IdeVersionCode: "20260811",
		DeviceBrand:    "CREFG-XX",
		OSVersion:      "Windows 11 Home China",
		Function:       "solo_work_lite",

		EpChat:          "/api/agent/v3/llm_utils_chat",
		EpModels:        "/api/ide/v1/get_detail_param",
		EpExchange:      "/cloudide/api/v3/trae/oauth/ExchangeToken",
		EpUserInfo:      "/cloudide/api/v3/trae/GetUserInfo",
		EpCheckinStatus: "/trae/api/v2/ug/checkin_credits/status",
		EpCheckinClaim:  "/trae/api/v2/ug/checkin_credits/claim",
		EpEntUsage:      "/trae/api/v2/pay/ide_user_ent_usage",
	}
}

// loadEndpoints 读 config.json，只覆盖用户显式写过的字段，其余保持默认。
func loadEndpoints(path string) (Endpoints, error) {
	eps := defaultEndpoints()
	if strings.TrimSpace(path) == "" {
		return eps, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return eps, nil
		}
		return eps, fmt.Errorf("读取接口配置失败: %w", err)
	}
	var user Endpoints
	if err := json.Unmarshal(raw, &user); err != nil {
		return eps, fmt.Errorf("解析接口配置 %s 失败: %w", path, err)
	}
	dst := reflect.ValueOf(&eps).Elem()
	src := reflect.ValueOf(user)
	for i := 0; i < src.NumField(); i++ {
		if src.Field(i).Kind() != reflect.String {
			continue
		}
		if v := strings.TrimSpace(src.Field(i).String()); v != "" {
			dst.Field(i).SetString(v)
		}
	}
	return eps, nil
}