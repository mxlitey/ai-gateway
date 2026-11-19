package adminmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/channel"
	"github.com/czg95/ai-gateway-v2/internal/id"
	"github.com/czg95/ai-gateway-v2/internal/mcp"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/plugin"
	"github.com/czg95/ai-gateway-v2/internal/pricing"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// WriteDeps 第二梯队（含写操作）所需依赖。
type WriteDeps struct {
	Store *store.Store
	Host  *plugin.Host
	MCP   *mcp.Service
	// HTTP 用于上游诊断，超时比普通请求宽松。
	HTTP *http.Client
	// Pricer 改价格后需要重建索引，否则新价格要等下次重启才生效。
	Pricer *pricing.Resolver
}

// RegisterWriteTools 注册第二梯队：3 个只读 + 9 个会触达或改动网关的工具。
//
// 其中 reset_key_cooldown / issue_api_key / create_channel / update_channel / restart_plugin /
// set_model_meta / delete_model_meta / set_price / delete_price 属于写操作，
// 只在读写令牌下可见可调用；只读令牌调用会直接报错。
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
	registerSetModelMetaTool(s, d)
	registerDeleteModelMetaTool(s, d)
	registerUpsertPriceTool(s, d)
	registerDeletePriceTool(s, d)
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
				ID             string           `json:"id"`
				Name           string           `json:"name"`
				Alias          string           `json:"alias"`
				URL            string           `json:"url"`
				Enabled        bool             `json:"enabled"`
				Source         string           `json:"source"`
				ToolCount      int              `json:"tool_count"`
				DailyCalls     int64            `json:"daily_calls"`
				DailyLimit     int              `json:"daily_limit"`
				LastStatus     string           `json:"last_status"`
				LastError      string           `json:"last_error"`
				Keys           []keyView        `json:"keys"`
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
				"image_models": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "生图模型列表（整体替换）；标记后这些模型只走 /v1/images/generations",
				},
				"image_generation": map[string]any{
					"type":                 "object",
					"description":          "生图配置，只覆盖传入的字段（未传的保持不变）。常用：timeout_ms、mode、poll_interval_ms、reference_field、reference_shape",
					"additionalProperties": true,
					"properties": map[string]any{
						"mode":                 map[string]any{"type": "string", "enum": []string{"", "sync", "async"}, "description": "sync 同步出图 / async 提交后轮询 / 留空按渠道类型判断"},
						"timeout_ms":           map[string]any{"type": "integer", "description": "生图整体超时（毫秒），异步模式用于轮询截止"},
						"sync_timeout_ms":      map[string]any{"type": "integer", "description": "同步模式等待上游响应的超时（毫秒），0 表示沿用 timeout_ms"},
						"poll_interval_ms":     map[string]any{"type": "integer", "description": "异步轮询间隔（毫秒）"},
						"response_format_mode": map[string]any{"type": "string", "enum": []string{"drop", "pass", "url", "b64_json"}},
						"reference_field":      map[string]any{"type": "string", "description": "参考图在上游请求体里的字段名"},
						"reference_shape":      map[string]any{"type": "string", "description": "参考图形态：url_array / single_url / url_object_array / image_url_object_array"},
					},
				},
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
			if _, ok := args["image_models"]; ok {
				next.ImageModels = argStrings(args, "image_models")
			}
			if raw, ok := args["image_generation"].(map[string]any); ok && len(raw) > 0 {
				merged, err := mergeImageGen(next.ImageGen, raw)
				if err != nil {
					return nil, err
				}
				next.ImageGen = merged
			}
			if err := d.Store.UpdateChannel(&next); err != nil {
				return nil, err
			}
			return map[string]any{"success": true, "channel_id": next.ID, "name": next.Name, "enabled": next.Enabled}, nil
		})
}

// mergeImageGen 以现有配置为底，用 patch 覆盖传入字段，再归一化默认值。
func mergeImageGen(cur model.ImageGenConfig, patch map[string]any) (model.ImageGenConfig, error) {
	base, err := json.Marshal(cur)
	if err != nil {
		return cur, err
	}
	fields := map[string]any{}
	if err := json.Unmarshal(base, &fields); err != nil {
		return cur, err
	}
	for k, v := range patch {
		fields[k] = v
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return cur, err
	}
	var out model.ImageGenConfig
	if err := json.Unmarshal(raw, &out); err != nil {
		return cur, fmt.Errorf("image_generation 不合法: %w", err)
	}
	return out.Normalized(), nil
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

// ---------- 写：保存生图模型元数据 ----------

func registerSetModelMetaTool(s *Server, d WriteDeps) {
	s.Tools("set_model_meta",
		"保存某个生图模型的能力与约束声明（整体覆盖，未传的字段会被清空）。这些声明只用于对外说明能力，网关不会据此拒绝请求。想删除某条声明请用 delete_model_meta。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"model":                map[string]any{"type": "string", "description": "公开模型名，必须与客户端请求里的 model 完全一致"},
				"description":          map[string]any{"type": "string", "description": "一句话说明，会展示给 AI 客户端"},
				"reference":            map[string]any{"type": "boolean", "description": "支持参考图（图生图/改图）"},
				"negative_prompt":      map[string]any{"type": "boolean", "description": "支持反向提示词 negative_prompt"},
				"seed":                 map[string]any{"type": "boolean", "description": "支持随机种子 seed"},
				"chinese_prompt":       map[string]any{"type": "boolean", "description": "能理解中文提示词"},
				"chinese_text":         map[string]any{"type": "boolean", "description": "能把中文文字正确画进图里"},
				"max_ref_count":        map[string]any{"type": "integer", "description": "参考图最多几张，0 表示不限"},
				"max_ref_side_px":      map[string]any{"type": "integer", "description": "参考图长边像素上限，0 表示不限"},
				"max_ref_mb":           map[string]any{"type": "integer", "description": "参考图单张体积上限（MB），0 表示不限"},
				"ref_formats":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"png", "jpeg", "webp"}}, "description": "允许的参考图格式，空表示不限制"},
				"max_n":                map[string]any{"type": "integer", "description": "不带参考图时 n 的上限，0 表示不限"},
				"max_n_ref":            map[string]any{"type": "integer", "description": "带参考图时 n 的上限，0 表示沿用 max_n"},
				"default_n":            map[string]any{"type": "integer", "description": "客户端未传 n 时的默认张数，0 表示由上游决定"},
				"sizes":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "支持的尺寸枚举，如 1024x1024"},
				"custom_size":          map[string]any{"type": "boolean", "description": "允许自定义尺寸"},
				"ignore_size_with_ref": map[string]any{"type": "boolean", "description": "带参考图时上游忽略 size"},
			},
			"required": []string{"model"},
		}, true, func(args map[string]any) (any, error) {
			name := argString(args, "model")
			if name == "" {
				return nil, fmt.Errorf("model 不能为空")
			}
			in := model.ModelMeta{
				Description:       argString(args, "description"),
				Reference:         argBool(args, "reference"),
				NegativePrompt:    argBool(args, "negative_prompt"),
				Seed:              argBool(args, "seed"),
				ChinesePrompt:     argBool(args, "chinese_prompt"),
				ChineseText:       argBool(args, "chinese_text"),
				MaxRefCount:       argInt(args, "max_ref_count", 0),
				MaxRefSidePx:      argInt(args, "max_ref_side_px", 0),
				MaxRefMB:          argInt(args, "max_ref_mb", 0),
				RefFormats:        argStrings(args, "ref_formats"),
				MaxN:              argInt(args, "max_n", 0),
				MaxNRef:           argInt(args, "max_n_ref", 0),
				DefaultN:          argInt(args, "default_n", 0),
				Sizes:             argStrings(args, "sizes"),
				CustomSize:        argBool(args, "custom_size"),
				IgnoreSizeWithRef: argBool(args, "ignore_size_with_ref"),
			}
			meta := model.SanitizeModelMeta(name, in)
			if err := d.Store.UpsertModelMeta(&meta); err != nil {
				return nil, err
			}
			if live, err := liveModelNames(d.Store); err == nil {
				meta.HasChannel = live[meta.Model]
			}
			return meta, nil
		})
}

// ---------- 写：删除生图模型元数据 ----------

func registerDeleteModelMetaTool(s *Server, d WriteDeps) {
	s.Tools("delete_model_meta",
		"删除某个生图模型的元数据声明。只删声明，不影响任何渠道、密钥或已上传的文件。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"model": map[string]any{"type": "string", "description": "公开模型名"},
			},
			"required": []string{"model"},
		}, true, func(args map[string]any) (any, error) {
			name := argString(args, "model")
			if name == "" {
				return nil, fmt.Errorf("model 不能为空")
			}
			if err := d.Store.DeleteModelMeta(name); err != nil {
				return nil, err
			}
			return map[string]any{"success": true, "model": name}, nil
		})
}

// ---------- 写：保存价格 ----------

func registerUpsertPriceTool(s *Server, d WriteDeps) {
	s.Tools("set_price",
		"新增或覆盖某个公开模型的计费规则。按 token 计费时必须给出各输入档位，且最后一档不设上限（up_to=0）；生图等按次计费的模型用 unit=per_request 并给 request_price。改完即时生效，只影响之后的请求。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"model": map[string]any{"type": "string", "description": "公开模型名"},
				"unit":  map[string]any{"type": "string", "enum": []string{"per_token", "per_request"}, "description": "计费单位，默认 per_token"},
				"tiers": map[string]any{
					"type":        "array",
					"description": "按 token 计费时的输入档位；最后一档 up_to 必须为 0",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"up_to":            map[string]any{"type": "integer", "description": "输入 token 不超过该值时适用此档，0 表示不限（只能出现在最后一档）"},
							"prompt_price":     map[string]any{"type": "number", "description": "输入单价（每百万 token）"},
							"completion_price": map[string]any{"type": "number", "description": "输出单价（每百万 token）"},
						},
					},
				},
				"cache_read_price": map[string]any{"type": "number", "description": "缓存命中输入的单价（每百万 token）"},
				"request_price":    map[string]any{"type": "number", "description": "每次请求单价，unit=per_request 时生效"},
				"time_windows": map[string]any{
					"type":        "array",
					"description": "峰谷时段系数，留空表示全天同价",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"start":  map[string]any{"type": "string", "description": "开始时间 HH:MM"},
							"end":    map[string]any{"type": "string", "description": "结束时间 HH:MM"},
							"factor": map[string]any{"type": "number", "description": "系数，小于 1 折扣、大于 1 加价"},
							"days":   map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "适用星期 0-6（0=周日），空表示每天"},
						},
					},
				},
				"note": map[string]any{"type": "string", "description": "备注"},
			},
			"required": []string{"model"},
		}, true, func(args map[string]any) (any, error) {
			name := argString(args, "model")
			if name == "" {
				return nil, fmt.Errorf("model 不能为空")
			}
			unit := argString(args, "unit")
			tiers := argPriceTiers(args, "tiers")
			windows := argPriceWindows(args, "time_windows")
			cachePrice := argFloat(args, "cache_read_price")
			requestPrice := argFloat(args, "request_price")
			if err := model.ValidatePrice(unit, tiers, cachePrice, requestPrice, windows); err != nil {
				return nil, err
			}

			key := pricing.Normalize(name)
			if key == "" {
				return nil, fmt.Errorf("model 不能为空")
			}
			p := &model.ModelPrice{
				Model:          key,
				Unit:           unit,
				Tiers:          tiers,
				CacheReadPrice: cachePrice,
				RequestPrice:   requestPrice,
				TimeWindows:    windows,
				Note:           argString(args, "note"),
			}
			if err := d.Store.UpsertModelPrice(p); err != nil {
				return nil, err
			}
			if d.Pricer != nil {
				if err := d.Pricer.Reload(); err != nil {
					return nil, fmt.Errorf("价格已保存，但重建价格索引失败: %w", err)
				}
			}
			return p, nil
		})
}

// ---------- 写：删除价格 ----------

func registerDeletePriceTool(s *Server, d WriteDeps) {
	s.Tools("delete_price",
		"删除某个公开模型的价格条目，删除后该模型回到「未定价」状态（不再估算费用，但调用不受影响）。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"model": map[string]any{"type": "string", "description": "公开模型名"},
			},
			"required": []string{"model"},
		}, true, func(args map[string]any) (any, error) {
			name := argString(args, "model")
			if name == "" {
				return nil, fmt.Errorf("model 不能为空")
			}
			key := pricing.Normalize(name)
			if err := d.Store.DeleteModelPrice(key); err != nil {
				return nil, err
			}
			if d.Pricer != nil {
				if err := d.Pricer.Reload(); err != nil {
					return nil, fmt.Errorf("价格已删除，但重建价格索引失败: %w", err)
				}
			}
			return map[string]any{"success": true, "model": key}, nil
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

// argFloat 读取浮点参数（JSON 数字统一解成 float64）。
func argFloat(args map[string]any, key string) float64 {
	v, ok := args[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	}
	return 0
}

// argPriceTiers 解析价格档位数组。
func argPriceTiers(args map[string]any, key string) []model.PriceTier {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]model.PriceTier, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, model.PriceTier{
			UpTo:            argInt(obj, "up_to", 0),
			PromptPrice:     argFloat(obj, "prompt_price"),
			CompletionPrice: argFloat(obj, "completion_price"),
		})
	}
	return out
}

// argPriceWindows 解析峰谷时段数组。
func argPriceWindows(args map[string]any, key string) []model.TimeWindow {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]model.TimeWindow, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		days := make([]int, 0, 7)
		if list, ok := obj["days"].([]any); ok {
			for _, d := range list {
				days = append(days, argInt(map[string]any{"v": d}, "v", 0))
			}
		}
		out = append(out, model.TimeWindow{
			Start:  argString(obj, "start"),
			End:    argString(obj, "end"),
			Factor: argFloat(obj, "factor"),
			Days:   days,
		})
	}
	return out
}
