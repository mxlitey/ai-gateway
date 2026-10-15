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
// 取值来源：trae2api/internal/upstream/constants.go（已实测）。
type Endpoints struct {
	AgentHost   string `json:"agent_host"`   // 对话 / 模型表
	UgHost      string `json:"ug_host"`      // 签到 / 积分
	OAuthHost   string `json:"oauth_host"`   // ExchangeToken / GetUserInfo
	ConsoleHost string `json:"console_host"` // 网页登录授权页

	ClientID       string `json:"client_id"`
	AppID          string `json:"app_id"`
	IdeVersion     string `json:"ide_version"`
	IdeVersionCode string `json:"ide_version_code"`
	DeviceBrand    string `json:"device_brand"`
	OSVersion      string `json:"os_version"`
	Function       string `json:"function"`

	EpChat          string `json:"ep_chat"`
	EpModels        string `json:"ep_models"`
	EpExchange      string `json:"ep_exchange"`
	EpUserInfo      string `json:"ep_user_info"`
	EpCheckinStatus string `json:"ep_checkin_status"`
	EpCheckinClaim  string `json:"ep_checkin_claim"`
}

func defaultEndpoints() Endpoints {
	return Endpoints{
		AgentHost:   "https://trae-api-cn.mchost.guru",
		UgHost:      "https://api.trae.cn",
		OAuthHost:   "https://api.trae.com.cn",
		ConsoleHost: "https://www.trae.cn",

		ClientID:       "en1oxy7wnw8j9n",
		AppID:          "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8",
		IdeVersion:     "0.1.52",
		IdeVersionCode: "20260811",
		DeviceBrand:    "Apple",
		OSVersion:      "macOS 15.7.4",
		Function:       "solo_work_lite",

		EpChat:          "/api/agent/v3/llm_utils_chat",
		EpModels:        "/api/ide/v1/get_detail_param",
		EpExchange:      "/cloudide/api/v3/trae/oauth/ExchangeToken",
		EpUserInfo:      "/cloudide/api/v3/trae/GetUserInfo",
		EpCheckinStatus: "/trae/api/v2/ug/checkin_credits/status",
		EpCheckinClaim:  "/trae/api/v2/ug/checkin_credits/claim",
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