package adminmcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/channel"
	"github.com/czg95/ai-gateway-v2/internal/id"
	"github.com/czg95/ai-gateway-v2/internal/mcp"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/plugin"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// WriteDeps 第二梯队（含写操作）所需依赖。
type WriteDeps struct {
	Store *store.Store
	Host  *plugin.Host
	MCP   *mcp.Service
	// HTTP 用于上游诊断，超时比普通请求宽松。
	HTTP *http.Client
}

// RegisterWriteTools 注册第二梯队：3 个只读 + 5 个会触达外部的工具。
//
// 其中 reset_key_cooldown / issue_api_key / create_channel / update_channel / restart_plugin
// 属于写操作，仅在「允许写操作」开启后可见可调用。
func RegisterWriteTools(s *Server, d WriteDeps) {
	registerListPluginsTool(s, d)
	registerListMCPServersTool(s, d)
	registerPluginConsoleTool(s, d)
	registerDiagnoseUpstreamTool(s, d)
	registerTestMCPServerTool(s, d)
	registerIssueAPIKeyTool(s, d)
	registerCreateChannelTool(s, d)
	registerUpdateChannelTool(s, d)
	registerRestartPluginTool(s, d)
}

// ---------- 只读：插件 ----------

func registerListPluginsTool(s *Server, d WriteDeps) {
	s.Tools("list_plugins",
		"列出所有 Python 插件：ID、名称、版本、运行状态、是否启用/自启、端口、声明的控制台能力与最后错误。用于回答「我有哪几个插件」「某个插件为什么挂了」。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plugin_id": map[string]any{"type": "string", "description": "只看某个插件，留空返回全部"},
			},
		}, false, func(args map[string]any) (any, error) {
		if d.Host == nil {
			return nil, fmt.Errorf("插件宿主未启用")
		}
		only := argString(args, "plugin_id")
		list := d.Host.List()
		out := make([]model.Plugin, 0, len(list))
		for _, p := range list {
			if only != "" && p.ID != only {
				continue
			}
			// Dir 是本地路径，对排查无帮助且泄露主机目录结构，置空
			p.Dir = ""
			out = append(out, p)
		}
		return map[string]any{"count": len(out), "plugins": out}, nil
	})
}

// ---------- 只读：MCP 上游 ----------

func registerListMCPServersTool(s *Server, d WriteDeps) {
	s.Tools("list_mcp_servers",
		"列出所有 MCP 上游服务器：名称、别名、URL、启用状态、工具数、今日调用次数、最后探测状态与错误。密钥只返回脱敏值与每日上限，不含明文。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"enabled_only": map[string]any{"type": "boolean", "description": "只看已启用的服务器"},
			},
		}, false, func(args map[string]any) (any, error) {
		servers, err := d.Store.ListMCPServers()
		if err != nil {
			return nil, err
		}
		counts, err := d.Store.CountMCPToolsByServer()
		if err != nil {
			counts = map[string]int{}
		}
		enabledOnly := argBool(args, "enabled_only")

		type keyView struct {
			Hint       string `json:"hint"`
			Label      string `json:"label,omitempty"`
			DailyLimit int    `json:"daily_limit"`
		}
		type serverView struct {
			ID             string         `json:"id"`
			Name           string         `json:"name"`
			Alias          string         `json:"alias"`
			URL            string         `json:"url"`
			Enabled        bool           `json:"enabled"`
			Source         string         `json:"source"`
			ToolCount      int            `json:"tool_count"`
			DailyCalls     int64          `json:"daily_calls"`
			DailyLimit     int            `json:"daily_limit"`
			LastStatus     string         `json:"last_status"`
			LastError      string         `json:"last_error"`
			Keys           []keyView      `json:"keys"`
			ToolDailyCalls map[string]int64 `json:"tool_daily_calls,omitempty"`
		}
		out := make([]serverView, 0, len(servers))
		for i := range servers {
			srv := &servers[i]
			if enabledOnly && !srv.Enabled {
				continue
			}
			keys := make([]keyView, 0, len(srv.Keys))
			for _, k := range srv.Keys {
				keys = append(keys, keyView{Hint: model.KeyHint(k.Key), Label: k.Label, DailyLimit: k.DailyLimit})
			}
			view := serverView{
				ID:         srv.ID,
				Name:       srv.Name,
				Alias:      srv.Alias,
				URL:        srv.URL,
				Enabled:    srv.Enabled,
				Source:     srv.Source,
				ToolCount:  counts[srv.ID],
				DailyLimit: srv.DailyLimit,
				Keys:       keys,
			}
			if d.MCP != nil {
				view.LastStatus, view.LastError = d.MCP.State(srv.ID)
				view.DailyCalls = d.MCP.DailyCalls(srv.ID)
				view.ToolDailyCalls = d.MCP.DailyCallsByTool(srv.ID)
			}
			out = append(out, view)
		}
		return map[string]any{"count": len(out), "servers": out}, nil
	})
}

// ---------- 只读：插件控制台地址 ----------

func registerPluginConsoleTool(s *Server, d WriteDeps) {
	s.Tools("get_plugin_console_url",
		"返回某个插件的控制台地址，供人工在浏览器打开完成账号登录等操作。插件未声明控制台或未运行时返回错误原因。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plugin_id": map[string]any{"type": "string", "description": "插件 ID"},
			},
			"required": []string{"plugin_id"},
		}, false, func(args map[string]any) (any, error) {
		if d.Host == nil {
			return nil, fmt.Errorf("插件宿主未启用")
		}
		pluginID := argString(args, "plugin_id")
		if pluginID == "" {
			return nil, fmt.Errorf("plugin_id 不能为空")
		}
		url, err := d.Host.ConsoleTarget(pluginID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"plugin_id": pluginID, "console_url": url}, nil
	})
}

// ---------- 会触达上游：诊断 ----------

// diagnoseResult 单个「渠道 × 密钥 × 模型」的探测结果。
type diagnoseResult struct {
	Model      string            `json:"model"`
	Channel    string            `json:"channel"`
	ChannelID  string            `json:"channel_id"`
	KeyHint    string            `json:"key_hint"`
	Status     int               `json:"status"`
	DurationMS int64             `json:"duration_ms"`
	RateLimit  map[string]string `json:"rate_headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// rateHeaderNames 诊断时回传的限流相关响应头。
var rateHeaderNames = []string{
	"retry-after", "x-ratelimit-limit-requests", "x-ratelimit-remaining-requests",
	"modelscope-ratelimit-requests-limit", "modelscope-ratelimit-requests-remaining",
	"modelscope-ratelimit-model-requests-limit", "modelscope-ratelimit-model-requests-remaining",
}

func registerDiagnoseUpstreamTool(s *Server, d WriteDeps) {
	s.Tools("diagnose_upstream",
		"真实请求上游以诊断某个模型/渠道/密钥的可用性：返回 HTTP 状态、耗时、限流头、响应片段与错误。不传任何参数时会遍历所有渠道 x 所有密钥 x 所有模型（会很慢并消耗大量上游配额），建议至少指定 model 或 channel_id。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"channel_id": map[string]any{"type": "string", "description": "渠道 ID，留空表示所有渠道"},
				"model":      map[string]any{"type": "string", "description": "公开模型名；留空时每个渠道都不会被探测（无法确定上游模型）"},
				"key":        map[string]any{"type": "string", "description": "上游密钥明文，留空表示该渠道所有启用的密钥"},
			},
		}, false, func(args map[string]any) (any, error) {
		channels, err := d.Store.ListChannels()
		if err != nil {
			return nil, err
		}
		targetChannel := argString(args, "channel_id")
		publicModel := argString(args, "model")
		targetKey := argString(args, "key")

		results := make([]diagnoseResult, 0, 8)
		for i := range channels {
			ch := &channels[i]
			if targetChannel != "" && ch.ID != targetChannel {
				continue
			}
			if targetChannel == "" && (!ch.Enabled || len(ch.EnabledKeys()) == 0) {
				continue
			}
			keys := ch.EnabledKeys()
			if targetKey != "" {
				filtered := make([]string, 0, 1)
				for _, k := range keys {
					if k == targetKey {
						filtered = append(filtered, k)
					}
				}
				keys = filtered
			}
			if len(keys) == 0 {
				continue
			}
			if publicModel == "" {
				results = append(results, diagnoseResult{
					Channel: ch.Name, ChannelID: ch.ID, Error: "未指定 model，无法诊断",
				})
				continue
			}
			upstreamModel, ok := ch.ResolveUpstreamModel(publicModel)
			if !ok {
				upstreamModel = publicModel
			}
			for _, key := range keys {
				results = append(results, probeOne(d.HTTP, ch, key, upstreamModel, publicModel))
			}
		}

		if len(results) == 0 {
			return nil, fmt.Errorf("没有可诊断的目标（渠道/密钥/模型不匹配）")
		}
		okCount, limited := 0, 0
		for _, r := range results {
			if r.Status == http.StatusOK && r.Error == "" {
				okCount++
			}
			if r.Status == http.StatusTooManyRequests {
				limited++
			}
		}
		return map[string]any{
			"total":        len(results),
			"ok":           okCount,
			"rate_limited": limited,
			"results":      results,
		}, nil
	})
}

// probeOne 对单个渠道 + 密钥 + 模型发起一次真实请求。
//
// 上游 SOLO 通道只接受流式请求，非流式会被判为异常调用（业务码 4011），
// 因此这里与真实客户端一致地使用 stream=true。
func probeOne(client *http.Client, ch *model.Channel, key, upstreamModel, publicModel string) diagnoseResult {
	res := diagnoseResult{
		Model:     publicModel,
		Channel:   ch.Name,
		ChannelID: ch.ID,
		KeyHint:   model.KeyHint(key),
	}

	payload := strings.NewReader(fmt.Sprintf(
		`{"model":%q,"messages":[{"role":"user","content":"say ok"}],"max_tokens":3,"stream":true}`,
		upstreamModel))
	testURL := channel.BaseURL(ch) + channel.ChatPath(ch)

	req, err := http.NewRequest(http.MethodPost, testURL, payload)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	channel.ApplyUpstreamKey(headers, ch, key)
	channel.ApplyChannelHeaders(headers, ch, nil)
	req.Header = headers

	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	start := time.Now()
	resp, err := client.Do(req)
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer func() { _ = resp.Body.Close() }()

	res.Status = resp.StatusCode
	buf := make([]byte, 300)
	n, _ := resp.Body.Read(buf)
	res.Body = strings.TrimSpace(string(buf[:n]))

	rate := map[string]string{}
	for _, name := range rateHeaderNames {
		if v := resp.Header.Get(name); v != "" {
			rate[name] = v
		}
	}
	if len(rate) > 0 {
		res.RateLimit = rate
	}
	return res
}

// ---------- 会触达上游：MCP 探测 ----------

func registerTestMCPServerTool(s *Server, d WriteDeps) {
	s.Tools("test_mcp_server",
		"真实连接某台 MCP 上游服务器，验证可达性与工具列表拉取是否正常，返回耗时与错误原因。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"server_id": map[string]any{"type": "string", "description": "MCP 服务器 ID（可从 list_mcp_servers 获取）"},
			},
			"required": []string{"server_id"},
		}, false, func(args map[string]any) (any, error) {
		if d.MCP == nil {
			return nil, fmt.Errorf("MCP 服务未启用")
		}
		serverID := argString(args, "server_id")
		if serverID == "" {
			return nil, fmt.Errorf("server_id 不能为空")
		}
		srv, err := d.Store.GetMCPServer(serverID)
		if err != nil {
			return nil, err
		}
		if srv == nil {
			return nil, fmt.Errorf("MCP 服务器不存在: %s", serverID)
		}
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := d.MCP.Probe(ctx, srv); err != nil {
			return map[string]any{
				"success":     false,
				"error":       err.Error(),
				"duration_ms": time.Since(start).Milliseconds(),
			}, nil
		}
		return map[string]any{"success": true, "duration_ms": time.Since(start).Milliseconds()}, nil
	})
}

// ---------- 写：签发 API 密钥 ----------

func registerIssueAPIKeyTool(s *Server, d WriteDeps) {
	s.Tools("issue_api_key",
		"签发一个客户端 API 密钥（sk- 开头）。注意：明文密钥只在本响应中返回一次，请立即保存。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":           map[string]any{"type": "string", "description": "密钥名称，便于区分用途"},
				"channel_scope":  map[string]any{"type": "string", "enum": []string{"all", "selected", "none"}, "description": "可用渠道范围，默认 all"},
				"channel_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "channel_scope=selected 时生效"},
				"mcp_scope":      map[string]any{"type": "string", "enum": []string{"all", "selected", "none"}, "description": "可用 MCP 范围，默认 all"},
				"mcp_server_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "mcp_scope=selected 时生效"},
			},
		}, true, func(args map[string]any) (any, error) {
		name := argString(args, "name")
		if name == "" {
			name = "Unnamed"
		}
		channelScope, channelIDs := normalizeScope(argString(args, "channel_scope"), argStrings(args, "channel_ids"))
		mcpScope, mcpIDs := normalizeScope(argString(args, "mcp_scope"), argStrings(args, "mcp_server_ids"))

		k := &model.APIKey{
			ID:           id.UUID(),
			Name:         name,
			Key:          id.APIKey(),
			ChannelScope: channelScope,
			ChannelIDs:   channelIDs,
			MCPScope:     mcpScope,
			MCPServerIDs: mcpIDs,
			Enabled:      true,
			CreatedAt:    time.Now().UnixMilli(),
		}
		if err := d.Store.CreateAPIKey(k); err != nil {
			return nil, err
		}
		return map[string]any{
			"id":             k.ID,
			"name":           k.Name,
			"key":            k.Key,
			"channel_scope":  k.ChannelScope,
			"channel_ids":    k.ChannelIDs,
			"mcp_scope":      k.MCPScope,
			"mcp_server_ids": k.MCPServerIDs,
			"notice":         "明文密钥只返回这一次，请立即保存",
		}, nil
	})
}

// ---------- 写：新建渠道 ----------

func registerCreateChannelTool(s *Server, d WriteDeps) {
	s.Tools("create_channel",
		"新建一个上游渠道（Base URL + 密钥 + 模型列表）。新渠道独立存在，不影响已有渠道。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":         map[string]any{"type": "string", "description": "渠道名称"},
				"base_url":     map[string]any{"type": "string", "description": "上游 Base URL，如 https://api.openai.com/v1"},
				"path":         map[string]any{"type": "string", "description": "聊天补全路径，留空用默认 /chat/completions"},
				"keys":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "上游密钥列表"},
				"models":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "该渠道支持的公开模型名"},
				"model_map":    map[string]any{"type": "object", "description": "公开模型名到上游模型名的映射"},
				"key_strategy": map[string]any{"type": "string", "enum": []string{"rotate", "ordered"}, "description": "多密钥策略，默认 rotate"},
				"enabled":      map[string]any{"type": "boolean", "description": "是否立即启用，默认 true"},
			},
			"required": []string{"name", "base_url"},
		}, true, func(args map[string]any) (any, error) {
		name := argString(args, "name")
		baseURL := argString(args, "base_url")
		if name == "" || baseURL == "" {
			return nil, fmt.Errorf("name 与 base_url 均不能为空")
		}
		order, err := d.Store.NextChannelSortOrder()
		if err != nil {
			return nil, err
		}
		enabled := true
		if v, ok := args["enabled"].(bool); ok {
			enabled = v
		}
		ch := &model.Channel{
			ID:            id.UUID(),
			Name:          name,
			BaseURL:       baseURL,
			Path:          argString(args, "path"),
			Keys:          toChannelKeys(argStrings(args, "keys")),
			Models:        argStrings(args, "models"),
			ModelMap:      argStringMap(args, "model_map"),
			ModelPriority: map[string]int{},
			KeyStrategy:   model.NormalizeKeyStrategy(argString(args, "key_strategy")),
			Enabled:       enabled,
			Source:        "manual",
			SortOrder:     order,
			CreatedAt:     time.Now().UnixMilli(),
		}
		if err := d.Store.CreateChannel(ch); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "channel_id": ch.ID, "name": ch.Name}, nil
	})
}

// ---------- 写：修改渠道 ----------

func registerUpdateChannelTool(s *Server, d WriteDeps) {
	s.Tools("update_channel",
		"修改已有渠道：名称、Base URL、路径、密钥、模型、映射、启用状态。只传需要改的字段，未传的保持不变。修改错误会导致该渠道调用全部失败，请谨慎。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"channel_id":   map[string]any{"type": "string", "description": "渠道 ID"},
				"name":         map[string]any{"type": "string", "description": "新名称"},
				"base_url":     map[string]any{"type": "string", "description": "新 Base URL"},
				"path":         map[string]any{"type": "string", "description": "新聊天补全路径"},
				"keys":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "完整的密钥列表（会整体替换）"},
				"models":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "完整的模型列表（会整体替换）"},
				"model_map":    map[string]any{"type": "object", "description": "完整的模型映射（会整体替换）"},
				"key_strategy": map[string]any{"type": "string", "enum": []string{"rotate", "ordered"}, "description": "多密钥策略"},
				"enabled":      map[string]any{"type": "boolean", "description": "是否启用"},
			},
			"required": []string{"channel_id"},
		}, true, func(args map[string]any) (any, error) {
		channelID := argString(args, "channel_id")
		if channelID == "" {
			return nil, fmt.Errorf("channel_id 不能为空")
		}
		existing, err := d.Store.GetChannel(channelID)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, fmt.Errorf("渠道不存在: %s", channelID)
		}
		next := *existing
		if v := argString(args, "name"); v != "" {
			next.Name = v
		}
		if v := argString(args, "base_url"); v != "" {
			next.BaseURL = v
		}
		if _, ok := args["path"]; ok {
			next.Path = argString(args, "path")
		}
		if _, ok := args["keys"]; ok {
			next.Keys = toChannelKeys(argStrings(args, "keys"))
		}
		if _, ok := args["models"]; ok {
			next.Models = argStrings(args, "models")
		}
		if _, ok := args["model_map"]; ok {
			next.ModelMap = argStringMap(args, "model_map")
		}
		if v := argString(args, "key_strategy"); v != "" {
			next.KeyStrategy = model.NormalizeKeyStrategy(v)
		}
		if v, ok := args["enabled"].(bool); ok {
			next.Enabled = v
		}
		if err := d.Store.UpdateChannel(&next); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "channel_id": next.ID, "name": next.Name, "enabled": next.Enabled}, nil
	})
}

// ---------- 写：重启插件 ----------

func registerRestartPluginTool(s *Server, d WriteDeps) {
	s.Tools("restart_plugin",
		"重启某个 Python 插件。会中断该插件上正在进行的请求，请确认确实需要。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plugin_id": map[string]any{"type": "string", "description": "插件 ID"},
			},
			"required": []string{"plugin_id"},
		}, true, func(args map[string]any) (any, error) {
		if d.Host == nil {
			return nil, fmt.Errorf("插件宿主未启用")
		}
		pluginID := argString(args, "plugin_id")
		if pluginID == "" {
			return nil, fmt.Errorf("plugin_id 不能为空")
		}
		if err := d.Host.Restart(pluginID); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "plugin_id": pluginID}, nil
	})
}

// ---------- 参数辅助 ----------

func argStrings(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			if t := strings.TrimSpace(s); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

func argStringMap(args map[string]any, key string) map[string]string {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	raw, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, val := range raw {
		if s, ok := val.(string); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(s)
		}
	}
	return out
}

func toChannelKeys(keys []string) []model.ChannelKey {
	out := make([]model.ChannelKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, model.ChannelKey{Key: k, Enabled: true})
	}
	return out
}

// normalizeScope 归一化权限范围：selected 需要至少一个 ID，否则降级为 none。
func normalizeScope(scope string, ids []string) (string, []string) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case model.ScopeNone:
		return model.ScopeNone, []string{}
	case model.ScopeSelected:
		if len(ids) == 0 {
			return model.ScopeNone, []string{}
		}
		return model.ScopeSelected, ids
	default:
		return model.ScopeAll, []string{}
	}
}
