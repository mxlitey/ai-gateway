package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// 对话转发：把网关进来的 OpenAI 请求转给上游的 chat 出口。
//
// 上游认「官方客户端身份」，所以必须带这套头（缺了会被当成非官方客户端拒掉）：
//
//	X-IDE-Type / X-IDE-Name / X-Product = WorkBuddy
//	X-Agent-Purpose: conversation
//	User-Agent: WorkBuddy/5.5.x …（与区域对应）
func chatHeaders(acc *Account, cfg realmConfig) map[string]string {
	ideVersion := "5.5.6"
	if cfg.Intl {
		ideVersion = "5.5.2"
	}
	return map[string]string{
		"Content-Type":    "application/json",
		"Accept":          "application/json, text/plain, */*",
		"X-Agent-Purpose": "conversation",
		"X-IDE-Name":      "WorkBuddy",
		"X-IDE-Type":      "WorkBuddy",
		"X-IDE-Version":   ideVersion,
		"X-Product":       "WorkBuddy",
		"X-Domain":        firstNonEmpty(acc.Domain, cfg.Domain),
		"User-Agent":      cfg.ChatUA,
		"X-User-Id":       acc.UID,
		"Authorization":   "Bearer " + acc.AccessToken,
		"X-Request-ID":    randomHex(16),
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// pickAccount 选一个可用账号（启用中且登录态没过期）。
func (p *Plugin) pickAccount() (*Account, error) {
	for _, a := range p.store.list() {
		if !a.Enabled {
			continue
		}
		if err := p.ensureToken(&a); err != nil {
			continue
		}
		return &a, nil
	}
	return nil, fmt.Errorf("没有可用账号：请先在插件控制台里导入或授权登录")
}

// bearerToken 取 Authorization: Bearer <token> 中的 token。
func bearerToken(header string) string {
	const prefix = "Bearer "
	v := strings.TrimSpace(header)
	if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
		return strings.TrimSpace(v[len(prefix):])
	}
	return ""
}

// accountForRequest 决定这次请求用哪个账号。
//
// 网关把渠道密钥放进 Authorization（账号模式下密钥就是账号 id），命中账号时直接用它——
// 「轮询 / 故障转移 / 启用停用」因此全部由网关的渠道密钥策略决定；没命中则回退到自动挑号，
// 兼容尚未同步账号的直连场景。
func (p *Plugin) accountForRequest(r *http.Request) (*Account, error) {
	if id := bearerToken(r.Header.Get("Authorization")); id != "" {
		if acc, ok := p.store.get(id); ok {
			if !acc.Enabled {
				return nil, fmt.Errorf("账号 %s 已在插件侧停用", firstNonEmpty(acc.Name, id))
			}
			if err := p.ensureToken(&acc); err != nil {
				return nil, fmt.Errorf("账号 %s 不可用：%w", firstNonEmpty(acc.Name, id), err)
			}
			return &acc, nil
		}
	}
	return p.pickAccount()
}

// forwardChat 把网关进来的请求转给上游。
//
// 上游只接受流式（非流式会返回 code=11101），所以这里统一以 stream=true 请求；
// 客户端要流式就照原样透传 SSE，要非流式就在本地把 SSE 聚合成一次响应。
func (p *Plugin) forwardChat(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	_ = r.Body.Close()

	var clientBody map[string]any
	if err := json.Unmarshal(raw, &clientBody); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	wantStream, _ := clientBody["stream"].(bool)
	clientBody["stream"] = true
	upstreamBody, err := json.Marshal(clientBody)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	acc, err := p.accountForRequest(r)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	cfg := p.realmCfg(acc.Site)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.ChatOrigin+p.eps.ChatCompletions, bytes.NewReader(upstreamBody))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for k, v := range chatHeaders(acc, cfg) {
		req.Header.Set(k, v)
	}

	resp, err := p.chat.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "请求上游失败: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if wantStream && resp.StatusCode == http.StatusOK {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, canFlush := w.(http.Flusher)
		buf := make([]byte, 8192)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if canFlush {
					flusher.Flush()
				}
			}
			if readErr != nil {
				return
			}
		}
	}

	// 非流式：读完整个 SSE 再聚合；上游报错时原样返回它的错误体
	full, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(full)
		return
	}
	merged, err := aggregateSSE(full)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "聚合上游流式响应失败: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(merged)
}

// aggregateSSE 把上游的 chat.completion.chunk 流合并成一次非流式响应。
func aggregateSSE(raw []byte) ([]byte, error) {
	var (
		id, model, finishReason string
		created                 int64
		content, reasoning      strings.Builder
		usage                   map[string]any
	)

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Created int64  `json:"created"`
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.ID != "" {
			id = chunk.ID
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Created > 0 {
			created = chunk.Created
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, c := range chunk.Choices {
			content.WriteString(c.Delta.Content)
			reasoning.WriteString(c.Delta.ReasoningContent)
			if c.FinishReason != "" {
				finishReason = c.FinishReason
			}
		}
	}

	if id == "" && created == 0 {
		return nil, fmt.Errorf("上游没有返回任何 chunk")
	}

	message := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	out := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": firstNonEmpty(finishReason, "stop"),
		}},
	}
	if usage != nil {
		out["usage"] = usage
	}
	return json.Marshal(out)
}

// handleModels 供网关「获取模型」使用：模型清单在产品配置的 data.agents[].models 里，
// 多个 agent 各带一组，这里合并去重。拉不到就返回空列表（不影响转发）。
func (p *Plugin) handleModels(w http.ResponseWriter, r *http.Request) {
	acc, err := p.accountForRequest(r)
	if err != nil {
		writeModels(w, nil)
		return
	}
	cfg := p.realmCfg(acc.Site)
	env, _, err := p.doJSON(http.MethodGet, cfg.ChatOrigin+p.eps.Models, nil, chatHeaders(acc, cfg))
	if err != nil || env.Code != 0 {
		writeModels(w, nil)
		return
	}
	writeModels(w, extractModels(decodeData(env.Data)))
}

// extractModels 从产品配置里取可用模型 id。
func extractModels(data map[string]any) []string {
	ids := make([]string, 0, 32)
	seen := map[string]bool{}

	add := func(v any) {
		s, ok := v.(string)
		if !ok {
			return
		}
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		ids = append(ids, s)
	}
	addList := func(v any) {
		if list, ok := v.([]any); ok {
			for _, item := range list {
				add(item)
			}
		}
	}

	// 兼容更直接的写法
	addList(data["models"])
	addList(data["availableModels"])

	if agents, ok := data["agents"].([]any); ok {
		for _, item := range agents {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			addList(m["models"])
			addList(m["availableModels"])
		}
	}
	sort.Strings(ids)
	return ids
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
	for _, id := range ids {
		out.Data = append(out.Data, modelEntry{ID: id, Object: "model", OwnedBy: "workbuddy"})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}

// runAsk 是排错用的最小实测：拿第一个账号向上游发一次对话，打印原始响应。
func runAsk(args []string) error {
	dataDir := ""
	prompt := "hi"
	stream := false
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.StringVar(&dataDir, "data", "", "插件数据目录")
	fs.StringVar(&prompt, "prompt", "hi", "提问内容")
	fs.BoolVar(&stream, "stream", true, "是否用流式（上游只接受流式）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if dataDir == "" {
		dataDir = defaultDataDir()
	}

	store, err := newAccountStore(dataDir)
	if err != nil {
		return err
	}
	eps, err := loadEndpoints("")
	if err != nil {
		return err
	}
	p := &Plugin{
		eps:   eps,
		store: store,
		data:  dataDir,
		http:  &http.Client{Timeout: 60 * time.Second},
		chat:  &http.Client{},
	}

	acc, err := p.pickAccount()
	if err != nil {
		return err
	}
	cfg := p.realmCfg(acc.Site)

	body := map[string]any{
		"model":    "auto",
		"stream":   stream,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	}
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.ChatOrigin+p.eps.ChatCompletions, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	for k, v := range chatHeaders(acc, cfg) {
		req.Header.Set(k, v)
	}

	resp, err := p.chat.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	out, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	log.Printf("HTTP %d  %s%s", resp.StatusCode, cfg.ChatOrigin, p.eps.ChatCompletions)
	text := strings.TrimSpace(string(out))
	if len(text) > 1200 {
		text = text[:1200] + " …（已截断）"
	}
	log.Printf("响应：%s", text)
	return nil
}