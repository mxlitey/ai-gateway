package adminmcp

import (
	"fmt"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/pricing"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// ReadDeps 只读工具需要的依赖。
type ReadDeps struct {
	Store  *store.Store
	Pricer *pricing.Resolver
}

// RegisterReadTools 注册第一梯队：9 个只读工具。
//
// 全部无副作用、不改状态，用于让 AI 自查「花了多少」「哪些模型可用」
// 「哪条密钥在冷却」「最近报了什么错」这类问题。
func RegisterReadTools(s *Server, d ReadDeps) {
	registerUsageTool(s, d)
	registerUsageLogsTool(s, d)
	registerUsageSeriesTool(s, d)
	registerPublicModelsTool(s, d)
	registerChannelsTool(s, d)
	registerCooldownsTool(s, d)
	registerResetCooldownTool(s, d)
	registerErrorLogsTool(s, d)
	registerPriceTool(s, d)
}

// ---------- 1. 用量汇总 ----------

func registerUsageTool(s *Server, d ReadDeps) {
	s.Tools("get_usage_summary",
		"查询指定时间区间内的用量与费用汇总：请求数、成功/失败数、各类 token 用量、总费用，并按模型、渠道、API 密钥、上游密钥、日期分组。默认最近 7 天。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"days":         map[string]any{"type": "integer", "description": "统计最近多少天，默认 7，1-365"},
				"from":         map[string]any{"type": "integer", "description": "起始时间（毫秒时间戳），优先于 days"},
				"to":           map[string]any{"type": "integer", "description": "结束时间（毫秒时间戳），默认现在"},
				"channel_name": map[string]any{"type": "string", "description": "按渠道名精确筛选合计数字"},
				"model":        map[string]any{"type": "string", "description": "按公开模型名精确筛选合计数字"},
				"key_hint":     map[string]any{"type": "string", "description": "按上游密钥脱敏值筛选合计数字"},
			},
		}, false, func(args map[string]any) (any, error) {
		days := clampInt(argInt(args, "days", 7), 1, 365)
		to := argInt64(args, "to", time.Now().UnixMilli())
		from := argInt64(args, "from", to-int64(days)*86400000)
		if from <= 0 || to <= 0 || from >= to {
			return nil, fmt.Errorf("时间区间不合法")
		}
		sum, err := d.Store.UsageSummary(from, to,
			argString(args, "channel_name"), argString(args, "model"), argString(args, "key_hint"))
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"from":         from,
			"to":           to,
			"days":         days,
			"summary":      sum,
			"price_source": pricing.Endpoint(),
		}, nil
	})
}

// ---------- 2. 用量明细 ----------

func registerUsageLogsTool(s *Server, d ReadDeps) {
	s.Tools("get_usage_logs",
		"分页查询逐条请求记录（时间倒序）：每次调用的模型、渠道、上游密钥、状态、耗时、token 用量与费用。用于定位具体哪次请求失败或花了多少钱。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"days":         map[string]any{"type": "integer", "description": "查询最近多少天，默认 7"},
				"from":         map[string]any{"type": "integer", "description": "起始时间（毫秒时间戳），优先于 days"},
				"to":           map[string]any{"type": "integer", "description": "结束时间（毫秒时间戳）"},
				"model":        map[string]any{"type": "string", "description": "按公开模型名筛选"},
				"channel_id":   map[string]any{"type": "string", "description": "按渠道 ID 筛选"},
				"channel_name": map[string]any{"type": "string", "description": "按渠道名筛选"},
				"api_key_id":   map[string]any{"type": "string", "description": "按客户端 API 密钥 ID 筛选"},
				"key_hint":     map[string]any{"type": "string", "description": "按上游密钥脱敏值筛选"},
				"only_fail":    map[string]any{"type": "boolean", "description": "只返回失败的请求"},
				"limit":        map[string]any{"type": "integer", "description": "返回条数，默认 50，最大 500"},
				"offset":       map[string]any{"type": "integer", "description": "分页偏移，默认 0"},
			},
		}, false, func(args map[string]any) (any, error) {
		days := clampInt(argInt(args, "days", 7), 1, 365)
		to := argInt64(args, "to", time.Now().UnixMilli())
		from := argInt64(args, "from", to-int64(days)*86400000)
		if from <= 0 || to <= 0 || from >= to {
			return nil, fmt.Errorf("时间区间不合法")
		}
		f := store.RequestLogFilter{
			FromMs:      from,
			ToMs:        to,
			Model:       argString(args, "model"),
			ChannelID:   argString(args, "channel_id"),
			ChannelName: argString(args, "channel_name"),
			APIKeyID:    argString(args, "api_key_id"),
			KeyHint:     argString(args, "key_hint"),
			OnlyFail:    argBool(args, "only_fail"),
			Limit:       clampInt(argInt(args, "limit", 50), 1, 500),
			Offset:      maxInt(argInt(args, "offset", 0), 0),
		}
		items, err := d.Store.ListRequestLogs(f)
		if err != nil {
			return nil, err
		}
		total, err := d.Store.CountRequestLogs(f)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"from":   from,
			"to":     to,
			"items":  items,
			"total":  total,
			"limit":  f.Limit,
			"offset": f.Offset,
		}, nil
	})
}

// ---------- 3. 用量趋势 ----------

func registerUsageSeriesTool(s *Server, d ReadDeps) {
	s.Tools("get_usage_series",
		"按时间桶返回用量趋势（请求数、token、费用），用于观察峰值与波动。bucket 可选 hour / day，默认 day。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"days":         map[string]any{"type": "integer", "description": "统计最近多少天，默认 7"},
				"bucket":       map[string]any{"type": "string", "enum": []string{"hour", "day"}, "description": "时间桶粒度，默认 day"},
				"channel_name": map[string]any{"type": "string", "description": "按渠道名筛选"},
				"model":        map[string]any{"type": "string", "description": "按模型名筛选"},
				"key_hint":     map[string]any{"type": "string", "description": "按上游密钥脱敏值筛选"},
			},
		}, false, func(args map[string]any) (any, error) {
		days := clampInt(argInt(args, "days", 7), 1, 365)
		bucket := strings.ToLower(argString(args, "bucket"))
		if bucket != "hour" {
			bucket = "day"
		}
		to := time.Now().UnixMilli()
		from := to - int64(days)*86400000
		buckets, requests, tokens, err := d.Store.UsageSeriesRange(
			from, to, bucket, argString(args, "channel_name"), argString(args, "model"), argString(args, "key_hint"))
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"from":         from,
			"to":           to,
			"bucket":       bucket,
			"buckets":      buckets,
			"total_requests": requests,
			"total_tokens": tokens,
		}, nil
	})
}

// ---------- 4. 可用模型 ----------

func registerPublicModelsTool(s *Server, d ReadDeps) {
	s.Tools("list_public_models",
		"列出当前所有可对外调用的公开模型，以及每个模型背后有哪些渠道、各自优先级与上游模型名。用于回答「我有哪些模型能用」「某个模型走了哪条渠道」。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"model": map[string]any{"type": "string", "description": "只看某个公开模型的详细路由，留空返回全部"},
			},
		}, false, func(args map[string]any) (any, error) {
		channels, err := d.Store.ListChannels()
		if err != nil {
			return nil, err
		}
		only := argString(args, "model")

		type route struct {
			ChannelID     string `json:"channel_id"`
			ChannelName   string `json:"channel_name"`
			Enabled       bool   `json:"enabled"`
			Priority      int    `json:"priority"`
			UpstreamModel string `json:"upstream_model"`
			ModelDisabled bool   `json:"model_disabled"`
			Direct        bool   `json:"direct"`
		}
		grouped := map[string][]route{}
		for i := range channels {
			ch := &channels[i]
			names := map[string]bool{}
			for pub := range ch.ModelMap {
				names[strings.TrimSpace(pub)] = true
			}
			for _, m := range ch.Models {
				names[strings.TrimSpace(m)] = true
			}
			for pub := range names {
				if pub == "" {
					continue
				}
				up, ok := ch.ResolveUpstreamModel(pub)
				if !ok {
					continue
				}
				_, mapped := ch.ModelMap[pub]
				grouped[pub] = append(grouped[pub], route{
					ChannelID:     ch.ID,
					ChannelName:   ch.Name,
					Enabled:       ch.Enabled && len(ch.EnabledKeys()) > 0 && !ch.IsModelDisabled(pub),
					Priority:      ch.Priority(pub),
					UpstreamModel: up,
					ModelDisabled: ch.IsModelDisabled(pub),
					Direct:        !mapped,
				})
			}
		}

		out := map[string][]route{}
		for name, routes := range grouped {
			if only != "" && name != only {
				continue
			}
			out[name] = routes
		}
		return map[string]any{"count": len(out), "models": out}, nil
	})
}

// ---------- 5. 渠道 ----------

func registerChannelsTool(s *Server, d ReadDeps) {
	s.Tools("list_channels",
		"列出所有上游渠道：名称、Base URL、启用状态、密钥数量与脱敏预览、可用模型、多密钥策略。上游密钥只返回脱敏值，不含明文。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"enabled_only": map[string]any{"type": "boolean", "description": "只看已启用的渠道"},
			},
		}, false, func(args map[string]any) (any, error) {
		channels, err := d.Store.ListChannels()
		if err != nil {
			return nil, err
		}
		enabledOnly := argBool(args, "enabled_only")

		type keyView struct {
			Hint    string `json:"hint"`
			Label   string `json:"label,omitempty"`
			Enabled bool   `json:"enabled"`
		}
		type channelView struct {
			ID            string            `json:"id"`
			Name          string            `json:"name"`
			BaseURL       string            `json:"base_url"`
			Path          string            `json:"path"`
			Enabled       bool              `json:"enabled"`
			Source        string            `json:"source"`
			KeyStrategy   string            `json:"key_strategy"`
			KeyCount      int               `json:"key_count"`
			Keys          []keyView         `json:"keys"`
			Models        []string          `json:"models"`
			ModelMap      map[string]string `json:"model_map"`
			DisabledModels []string         `json:"disabled_models"`
		}
		out := make([]channelView, 0, len(channels))
		for i := range channels {
			ch := &channels[i]
			if enabledOnly && !ch.Enabled {
				continue
			}
			keys := make([]keyView, 0, len(ch.Keys))
			for _, k := range ch.Keys {
				keys = append(keys, keyView{Hint: model.KeyHint(k.Key), Label: k.Label, Enabled: k.Enabled})
			}
			out = append(out, channelView{
				ID:             ch.ID,
				Name:           ch.Name,
				BaseURL:        ch.BaseURL,
				Path:           ch.Path,
				Enabled:        ch.Enabled,
				Source:         ch.Source,
				KeyStrategy:    ch.KeyStrategy,
				KeyCount:       len(ch.Keys),
				Keys:           keys,
				Models:         ch.Models,
				ModelMap:       ch.ModelMap,
				DisabledModels: ch.DisabledModels,
			})
		}
		return map[string]any{"count": len(out), "channels": out}, nil
	})
}

// ---------- 6. 密钥冷却 ----------

func registerCooldownsTool(s *Server, d ReadDeps) {
	s.Tools("list_key_cooldowns",
		"列出当前处于失败冷却中的上游密钥：所属渠道、脱敏密钥、连续失败次数、剩余冷却秒数。用于回答「为什么某个模型突然不用某条密钥了」。",
		map[string]any{"type": "object", "properties": map[string]any{}},
		false, func(args map[string]any) (any, error) {
			items, err := d.Store.ListActiveKeyCooldowns(time.Now().UnixMilli())
			if err != nil {
				return nil, err
			}
			return map[string]any{"count": len(items), "items": items}, nil
		})
}

// ---------- 7. 重置冷却 ----------

func registerResetCooldownTool(s *Server, d ReadDeps) {
	s.Tools("reset_key_cooldown",
		"手动解除某条上游密钥的冷却，使其立即重新参与选路。参数为渠道 ID 与脱敏密钥（可从 list_key_cooldowns 获取）。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"channel_id": map[string]any{"type": "string", "description": "渠道 ID"},
				"key_hint":   map[string]any{"type": "string", "description": "上游密钥的脱敏值，如 abcd123...wxyz"},
			},
			"required": []string{"channel_id", "key_hint"},
		}, true, func(args map[string]any) (any, error) {
		channelID := argString(args, "channel_id")
		keyHint := argString(args, "key_hint")
		if channelID == "" || keyHint == "" {
			return nil, fmt.Errorf("channel_id 与 key_hint 均不能为空")
		}
		if err := d.Store.ClearKeyCooldown(channelID, keyHint); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "channel_id": channelID, "key_hint": keyHint}, nil
	})
}

// ---------- 8. 错误日志 ----------

func registerErrorLogsTool(s *Server, d ReadDeps) {
	s.Tools("get_error_logs",
		"查询上游错误日志（按渠道、时间区间）：HTTP 状态、错误信息、发生时间。用于排查最近的失败原因。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"channel_id": map[string]any{"type": "string", "description": "渠道 ID；留空返回所有渠道"},
				"days":       map[string]any{"type": "integer", "description": "查询最近多少天，默认 7"},
			},
		}, false, func(args map[string]any) (any, error) {
		days := clampInt(argInt(args, "days", 7), 1, 365)
		to := time.Now().UnixMilli()
		from := to - int64(days)*86400000
		channelID := argString(args, "channel_id")
		if channelID == "" {
			channels, err := d.Store.ListChannels()
			if err != nil {
				return nil, err
			}
			all := []model.ErrorLog{}
			for i := range channels {
				logs, err := d.Store.ListErrorLogsByChannel(channels[i].ID, from, to)
				if err != nil {
					return nil, err
				}
				all = append(all, logs...)
			}
			return map[string]any{"from": from, "to": to, "count": len(all), "items": all}, nil
		}
		logs, err := d.Store.ListErrorLogsByChannel(channelID, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"from": from, "to": to, "count": len(logs), "items": logs}, nil
	})
}

// ---------- 9. 模型价格 ----------

func registerPriceTool(s *Server, d ReadDeps) {
	s.Tools("get_price",
		"查询某个模型的价格（每百万 token 的输入/输出/缓存读价格），以及价格表来源与快照时间。模型名支持模糊匹配（大小写、厂商前缀会归一化）。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"model": map[string]any{"type": "string", "description": "模型名，如 gpt-4o、claude-sonnet-4"},
				"limit": map[string]any{"type": "integer", "description": "未指定 model 时返回多少个低价/高价样本，默认 20"},
			},
		}, false, func(args map[string]any) (any, error) {
		name := argString(args, "model")
		if name != "" && d.Pricer != nil {
			if p, ok := d.Pricer.Resolve(name); ok {
				return map[string]any{"found": true, "price": p}, nil
			}
			return map[string]any{
				"found":  false,
				"model":  name,
				"reason": "价格表中没有该模型，可调用不分组的 list 查看已收录的模型",
			}, nil
		}

		all, err := d.Store.ListModelPrices()
		if err != nil {
			return nil, err
		}
		limit := clampInt(argInt(args, "limit", 20), 1, 200)
		if len(all) > limit {
			all = all[:limit]
		}
		source := ""
		if v, ok, _ := d.Store.GetSetting(store.SettingPriceRefreshAt); ok {
			source = v
		}
		return map[string]any{
			"price_source": pricing.Endpoint(),
			"refreshed_at": source,
			"count":        len(all),
			"prices":       all,
		}, nil
	})
}

// ---------- 参数取值辅助 ----------

func argString(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func argBool(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "1" || strings.EqualFold(t, "true")
	}
	return false
}

func argInt(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	}
	return def
}

func argInt64(args map[string]any, key string, def int64) int64 {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	}
	return def
}

func clampInt(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
