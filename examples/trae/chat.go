package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// 对话转发 + 模型表 + 选号。
//
// 上游只接受流式，所以插件统一以 stream=true 请求上游：
// 客户端要流式就边收边发（SSE），要非流式就在本地聚合出一个完整 chat.completion。
//
// 选号：X-Plugin-Account 命中且启用 → 用它；命中但已停用 → 403；头缺失 → 回退自动挑号（仅直连调试）。
// 插件**不做任何调度**（不按积分挑号、不做冷却降级、不跳号），选择哪个账号全部归网关。

// accountForRequest 决定这次请求用哪个账号。
func (p *Plugin) accountForRequest(r *http.Request) (*Account, error) {
	if id := strings.TrimSpace(r.Header.Get("X-Plugin-Account")); id != "" {
		if acc, ok := p.store.get(id); ok {
			if !acc.usable() {
				return nil, &upstreamError{Status: http.StatusForbidden, Msg: "账号 " + acc.name() + " 已在插件侧停用"}
			}
			if err := p.ensureToken(&acc); err != nil {
				return nil, err
			}
			return &acc, nil
		}
	}
	return p.pickAccount()
}

// pickAccount 回退挑号：取第一个可用（启用且能续期）的账号。仅用于直连调试。
func (p *Plugin) pickAccount() (*Account, error) {
	for _, a := range p.store.list() {
		if !a.usable() {
			continue
		}
		acc := a
		if err := p.ensureToken(&acc); err != nil {
			continue
		}
		return &acc, nil
	}
	return nil, &upstreamError{Status: http.StatusServiceUnavailable, Msg: "没有可用账号：请先在插件控制台登录或导入 TRAE 账号"}
}

// injectSoloFields 注入 llm_utils_chat 的官方客户端必填字段。
//
// 依据 AgentHub proxy/adapters.cjs 的 rewriteBody()：官方客户端除 stream / function /
// config_name / model 外还会带一组 uuid 会话标识与设备/版本指纹，缺这些字段会被上游
// 识别为非官方调用。默认值取其一（max_tokens 4096、prompt_max_tokens 168000、
// workspace_id "e04cdd"）。
func (p *Plugin) injectSoloFields(body map[string]any, a *Account) {
	// 客户端已给的正数优先，否则用官方默认值（AgentHub 同判据）。
	if n := firstPositive(body["max_tokens"], body["max_completion_tokens"]); n > 0 {
		body["max_tokens"] = n
	} else {
		body["max_tokens"] = 4096
	}
	if n := firstPositive(body["prompt_max_tokens"], body["context_length"]); n > 0 {
		body["prompt_max_tokens"] = n
	} else {
		body["prompt_max_tokens"] = 168000
	}
	body["conversation_id"] = randomUUID()
	body["session_id"] = randomUUID()
	body["project_id"] = randomUUID()
	body["user_id"] = a.UID
	body["device_id"] = a.DeviceID
	body["machine_id"] = a.MachineID
	body["workspace_id"] = "e04cdd"
	body["mode"] = "FunctionCall"
	body["ide_version"] = p.eps.IdeVersion
	body["ide_version_code"] = p.eps.IdeVersionCode
	body["app_id"] = p.eps.AppID
	body["package_type"] = "stable_cn"
}

// forwardChat 把网关转来的 OpenAI 请求落到 TRAE SOLO 上游。
func (p *Plugin) forwardChat(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	_ = r.Body.Close()

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	wantStream, _ := body["stream"].(bool)
	model, _ := body["model"].(string)
	model = strings.TrimSpace(model)

	// 上游只接受流式；把 model 归一化为 config_name / model。
	body["stream"] = true
	if model == "" || strings.EqualFold(model, "auto") {
		model = defaultModel()
	}
	body["function"] = p.eps.Function
	body["config_name"] = model
	body["model"] = model
	prepareMessages(body)
	normalizeToolChoice(body)
	normalizeTools(body)

	acc, err := p.accountForRequest(r)
	if err != nil {
		writeUpstreamErr(w, err)
		return
	}
	// 账号级必填字段（device_id / machine_id / user_id）必须在 marshal 之前注入。
	p.injectSoloFields(body, acc)

	prepared, err := json.Marshal(body)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "构造上游请求失败")
		return
	}

	// 官方域优先，连接层失败或 404 时回退镜像域（AgentHub：官方域随时可能停 agent 服务）。
	// ChatMirror 默认为空 → 只有一个候选，行为与单域完全一致。
	candidates := []string{p.eps.AgentHost + p.eps.EpChat}
	if m := strings.TrimSpace(p.eps.ChatMirror); m != "" {
		candidates = append(candidates, m)
	}
	var resp *http.Response
	for i, target := range candidates {
		req, rerr := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(prepared))
		if rerr != nil {
			writeErr(w, http.StatusInternalServerError, "构造上游请求失败")
			return
		}
		p.soloHeaders(req, acc, true)
		req.Header.Set("Referer", target) // 与请求 URL 同源（AgentHub 实证：伪装同源请求）
		r2, derr := p.stream.Do(req)
		if derr != nil {
			if i == len(candidates)-1 {
				writeErr(w, http.StatusBadGateway, "请求上游失败："+truncateRunes(derr.Error(), 80))
				return
			}
			continue
		}
		if r2.StatusCode == http.StatusNotFound && i < len(candidates)-1 {
			_ = r2.Body.Close()
			continue
		}
		resp = r2
		break
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		upBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		ue := classifyUpstream(resp.StatusCode, string(upBody))
		p.noteUpstreamError(acc, ue)
		writeErr(w, ue.Status, ue.Msg)
		return
	}

	if wantStream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		// notify_usage 里的 ide_credits 是 SOLO 专属口径，但只是「对话那一刻」的瞬时值，
		// 不作为面板积分来源（积分只在签到/额度刷新时查询，见 console.go）。
		wroteAny, _, serr, ioErr := streamSOLO(w, resp.Body, model)
		if serr != nil && !wroteAny {
			// 尚未写出任何 chunk：仍可用非 2xx + {'error':...} 回复（响应头还没发出去）。
			p.noteUpstreamError(acc, serr)
			writeErr(w, serr.Status, serr.Msg)
			return
		}
		if ioErr != nil {
			return // 连接层错误：客户端已断开或上游中断，无法再改状态码
		}
		p.noteSuccess(acc.UID)
		return
	}

	// 非流式：读完整个 SSE 再聚合（成功响应必须带非空 choices）。
	out, _, serr, aggErr := aggregateSOLO(resp.Body, model)
	if serr != nil {
		p.noteUpstreamError(acc, serr)
		writeErr(w, serr.Status, serr.Msg)
		return
	}
	if aggErr != nil {
		writeErr(w, http.StatusBadGateway, "聚合上游响应失败")
		return
	}
	choices, _ := out["choices"].([]any)
	if len(choices) == 0 {
		writeErr(w, http.StatusBadGateway, "上游未返回任何内容")
		return
	}
	p.noteSuccess(acc.UID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// prepareMessages 把 OpenAI messages 的 content 字符串改写为上游要求的 [{type:text,text}]。
func prepareMessages(body map[string]any) {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return
	}
	for _, mi := range msgs {
		m, ok := mi.(map[string]any)
		if !ok {
			continue
		}
		// assistant 回传的 tool_calls：OpenAI 的 function → 上游的 function_call
		if role, _ := m["role"].(string); role == "assistant" {
			if tcs, ok := m["tool_calls"].([]any); ok {
				kept := make([]any, 0, len(tcs))
				for _, tci := range tcs {
					tc, ok := tci.(map[string]any)
					if !ok {
						continue
					}
					if fn, ok := tc["function"].(map[string]any); ok {
						tc["function_call"] = fn
						delete(tc, "function")
					}
					if fc, ok := tc["function_call"].(map[string]any); ok {
						if name, _ := fc["name"].(string); strings.TrimSpace(name) == "" {
							continue // 上游要求 FunctionCall.Name 必填
						}
					}
					kept = append(kept, tc)
				}
				if len(kept) == 0 {
					delete(m, "tool_calls")
				} else {
					m["tool_calls"] = kept
				}
			}
		}
		content, present := m["content"]
		if !present || content == nil {
			continue
		}
		if s, ok := content.(string); ok {
			m["content"] = []any{map[string]any{"type": "text", "text": s}}
		}
	}
}

// normalizeToolChoice 按上游 Go struct（字符串类型）改写 OpenAI tool_choice。
func normalizeToolChoice(obj map[string]any) {
	suppress := func() {
		delete(obj, "tools")
		delete(obj, "functions")
	}
	tc, present := obj["tool_choice"]
	if !present {
		return
	}
	switch v := tc.(type) {
	case string:
		if strings.EqualFold(strings.TrimSpace(v), "none") {
			delete(obj, "tool_choice")
			suppress()
		}
	case map[string]any:
		typ, _ := v["type"].(string)
		switch strings.ToLower(strings.TrimSpace(typ)) {
		case "none":
			delete(obj, "tool_choice")
			suppress()
		case "auto", "required":
			obj["tool_choice"] = strings.ToLower(strings.TrimSpace(typ))
		case "function":
			name := ""
			if fn, ok := v["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
			}
			if name == "" {
				name, _ = v["name"].(string)
			}
			if name = strings.TrimSpace(name); name != "" {
				obj["tool_choice"] = name
			} else {
				obj["tool_choice"] = "auto"
			}
		default:
			delete(obj, "tool_choice")
		}
	default:
		delete(obj, "tool_choice")
	}
}

// normalizeTools 把 OpenAI tools 的 function.parameters（object）序列化为上游要求的字符串。
func normalizeTools(obj map[string]any) {
	raw, present := obj["tools"]
	if !present {
		return
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return
	}
	out := make([]any, 0, len(list))
	for _, item := range list {
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := t["function"].(map[string]any)
		if !ok {
			continue
		}
		if params, ok := fn["parameters"].(map[string]any); ok {
			if s, err := json.Marshal(params); err == nil {
				fn["parameters"] = string(s)
			}
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		delete(obj, "tools")
		return
	}
	obj["tools"] = out
}

// ---------- 模型表 ----------

// staticModels 是拉不到上游模型表时的回退清单（设计稿 §4.1 要求回退静态列表）。
var staticModels = []string{"glm-5.2", "glm-5.3"}

func (p *Plugin) handleModels(w http.ResponseWriter, r *http.Request) {
	ids := staticModels
	if acc, err := p.accountForRequest(r); err == nil {
		if models, err := p.fetchModels(acc); err == nil {
			ids = make([]string, 0, len(models))
			for _, m := range models {
				ids = append(ids, m.ID)
			}
			p.noteSuccess(acc.UID)
		}
	}
	writeModels(w, ids)
}

func writeModels(w http.ResponseWriter, ids []string) {
	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}{Object: "list", Data: []modelEntry{}}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out.Data = append(out.Data, modelEntry{ID: id, Object: "model", OwnedBy: "trae-solo"})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// ---------- 状态上报（不是调度） ----------

// noteUpstreamError 记录上游错误：登录态失效时把账号硬禁用，并写脱敏后的 last_error。
func (p *Plugin) noteUpstreamError(acc *Account, ue *upstreamError) {
	if acc == nil || ue == nil {
		return
	}
	p.store.updateState(acc.UID, func(st *accountState) {
		if ue.Disable {
			st.Enabled = false
			st.HardDisabled = true
		}
		st.LastError = sanitizeError(ue.Msg)
	})
}

// noteSuccess 清掉上次的错误提示。
func (p *Plugin) noteSuccess(uid string) {
	p.store.updateState(uid, func(st *accountState) { st.LastError = "" })
}