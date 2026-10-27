// Package proxy 实现 /v1 转发：密钥校验、目标选择、故障转移与流式修补。
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/channel"
	"github.com/czg95/ai-gateway-v2/internal/id"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/penalty"
	"github.com/czg95/ai-gateway-v2/internal/pricing"
	"github.com/czg95/ai-gateway-v2/internal/routing"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// Handler 代理处理器。
type Handler struct {
	store    *store.Store
	client   *http.Client
	balancer *routing.Balancer
	pricer   *pricing.Resolver
	cooler   *penalty.Cooler
}

// New 创建代理处理器。pricer 可为 nil（不估算费用）。
func New(s *store.Store, pricer *pricing.Resolver) *Handler {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// SSE 不能压缩，否则代理会缓冲整个流
		DisableCompression: true,
	}
	balancer := routing.New(s)
	cooler := penalty.New(s, penalty.DefaultConfig())
	// 选路时严格屏蔽冷却中的密钥
	balancer.SetCoolingChecker(cooler.Cooling)
	return &Handler{
		store:    s,
		balancer: balancer,
		pricer:   pricer,
		cooler:   cooler,
		// 不设置 Client.Timeout：长连接流式不能被总超时打断
		client: &http.Client{Transport: transport},
	}
}

// coolerConfig 读取当前生效的密钥冷却规则（面板可改，热生效）。
func (h *Handler) coolerConfig() penalty.Config {
	cfg := penalty.DefaultConfig()
	if h.store == nil {
		return cfg
	}
	rs, err := h.store.LoadRuntimeSettings()
	if err != nil {
		return cfg
	}
	cfg.Enabled = rs.KeyCooldownEnabled
	if rs.KeyCooldownBaseSeconds > 0 {
		cfg.Base = time.Duration(rs.KeyCooldownBaseSeconds) * time.Second
	}
	if rs.KeyCooldownMaxSeconds > 0 {
		cfg.Max = time.Duration(rs.KeyCooldownMaxSeconds) * time.Second
	}
	return cfg
}

// Cooler 暴露密钥冷却器，供管理接口查询与手动重置。
func (h *Handler) Cooler() *penalty.Cooler { return h.cooler }

// Handle 处理 /v1/* 请求。
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	apiKey, errMsg := h.VerifyAPIKey(r)
	if apiKey == nil {
		jsonError(w, http.StatusUnauthorized, errMsg, "invalid_request_error")
		return
	}

	path := r.URL.Path
	allowed := apiKey.ChannelIDs

	if strings.HasSuffix(path, "/models") && r.Method == http.MethodGet {
		h.handleModels(w, allowed)
		return
	}

	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "Method not allowed", "invalid_request_error")
		return
	}

	body, err := decodeJSONObject(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON body", "invalid_request_error")
		return
	}

	modelName, _ := body["model"].(string)
	isStream, _ := body["stream"].(bool)
	rec := newRecorder(h.store, h.pricer, apiKey, endpointOf(path), isStream, modelName)
	// 兜底：任何提前返回都会结算一次（仅成功的请求落用量明细，失败走错误日志）
	defer rec.Commit()

	if strings.HasSuffix(path, "/messages") {
		h.handleClaude(w, r, body, allowed, rec)
		return
	}
	h.handleOpenAI(w, r, body, allowed, rec)
}

// VerifyAPIKey 校验客户端密钥，返回密钥对象与错误信息。
func (h *Handler) VerifyAPIKey(r *http.Request) (*model.APIKey, string) {
	key := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		key = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if key == "" {
		key = r.Header.Get("x-api-key")
	}
	if key == "" {
		return nil, "Missing API key (Authorization or x-api-key header)"
	}

	k, err := h.store.FindEnabledAPIKeyByValue(key)
	if err != nil {
		slog.Error("查询客户端密钥失败", "err", err)
		return nil, "Invalid or disabled API key"
	}
	if k == nil {
		return nil, "Invalid or disabled API key"
	}
	return k, ""
}

// ---------- 故障转移 ----------

// fwdSpec 描述一次转发流程。
type fwdSpec struct {
	targets            []routing.Target
	publicModel        string
	body               map[string]any
	rawQuery           string
	buildPath          func(t routing.Target) string
	applyModel         func(t routing.Target)
	isStream           bool
	acceptStreamHeader bool
	// handleUpstream 处理「上游有响应且 <500」的情况；返回 retry=true 表示继续下一个目标
	handleUpstream func(resp *http.Response, t routing.Target) (out *http.Response, retry bool, reason string)
	// allFailed 生成全部目标失败后的响应
	allFailed func(lastErr, last429 string) *http.Response
	// rec 用量与费用记录器（可为 nil）
	rec *Recorder
}

// forward 依次尝试目标，遇到 404/429/5xx/网络错误/无效响应则切换下一个。
func (h *Handler) forward(ctx context.Context, clientReq *http.Request, spec fwdSpec) *http.Response {
	var lastErr, last429 string
	cdCfg := h.coolerConfig()

	for _, t := range spec.targets {
		spec.rec.Attempt()

		targetURL := channel.BaseURL(t.Channel) + spec.buildPath(t)
		if spec.rawQuery != "" {
			targetURL += "?" + spec.rawQuery
		}

		slog.Debug("[proxy] -> target", "channel", t.Channel.Name, "url", targetURL)

		if spec.applyModel != nil {
			spec.applyModel(t)
		}
		payload, err := json.Marshal(spec.body)
		if err != nil {
			lastErr = "marshal body: " + err.Error()
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
		if err != nil {
			lastErr = "build request: " + err.Error()
			continue
		}

		headers := http.Header{}
		channel.ApplyPassthroughHeaders(headers, clientReq)
		headers.Set("Content-Type", "application/json")
		channel.ApplyUpstreamKey(headers, t.Channel, t.Key)
		if spec.isStream && spec.acceptStreamHeader {
			headers.Set("Accept", "text/event-stream")
		}
		channel.ApplyChannelHeaders(headers, t.Channel, clientReq)
		req.Header = headers

		resp, err := h.client.Do(req)
		if err != nil {
			lastErr = "network error: " + err.Error()
			h.cooler.Fail(t.Channel.ID, t.Key, cdCfg, 0)
			h.logError(t, spec.publicModel, 0, lastErr+" ("+targetURL+")")
			continue
		}

		switch {
		case resp.StatusCode == http.StatusNotFound:
			lastErr = "HTTP 404 (model not found)"
			h.cooler.Fail(t.Channel.ID, t.Key, cdCfg, 404)
			h.logError(t, spec.publicModel, 404, lastErr)
			closeBody(resp)
			continue

		case resp.StatusCode == http.StatusTooManyRequests:
			last429 = readBodyLimited(resp, 4096)
			lastErr = "HTTP 429 (rate limited)"
			h.cooler.Fail(t.Channel.ID, t.Key, cdCfg, 429)
			h.logError(t, spec.publicModel, 429, lastErr)
			closeBody(resp)
			continue

		// 凭据/权限被上游拒绝：按"这个账号不可用"处理，换下一个目标
		// （与 docs/plugin-spec.md §5.2 的错误语义一致）
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			lastErr = "HTTP " + strconv.Itoa(resp.StatusCode) + " (credential rejected)"
			h.cooler.Fail(t.Channel.ID, t.Key, cdCfg, resp.StatusCode)
			h.logError(t, spec.publicModel, resp.StatusCode, lastErr)
			closeBody(resp)
			continue

		case resp.StatusCode < 500:
			out, retry, reason := spec.handleUpstream(resp, t)
			if retry {
				closeBody(resp)
				lastErr = reason
				h.cooler.Fail(t.Channel.ID, t.Key, cdCfg, resp.StatusCode)
				continue
			}
			spec.rec.Served(t)
			h.cooler.Success(t.Channel.ID, t.Key)
			return out

		default:
			lastErr = "HTTP " + strconv.Itoa(resp.StatusCode)
			h.cooler.Fail(t.Channel.ID, t.Key, cdCfg, resp.StatusCode)
			h.logError(t, spec.publicModel, resp.StatusCode, lastErr)
			closeBody(resp)
			continue
		}
	}

	return spec.allFailed(lastErr, last429)
}

// validateUpstreamResult 校验 HTTP 200 的响应体是否实为失败。
// 返回非空字符串表示应重试（同时已记入错误日志）。
func (h *Handler) validateUpstreamResult(raw []byte, t routing.Target, publicModel string) string {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		return "" // 非 JSON，原样透传
	}
	// 插件的错误体是 {"error": "文案"}（字符串），上游多为 {"error": {...}}（对象），两者都要认
	if s, ok := data["error"].(string); ok && s != "" {
		msg := "HTTP 200 with error: " + s
		h.logError(t, publicModel, 200, msg)
		return msg
	}
	if e, ok := data["error"].(map[string]any); ok {
		msg := "HTTP 200 with error: " + errorMessage(e)
		h.logError(t, publicModel, 200, msg)
		return msg
	}
	choices, ok := data["choices"].([]any)
	if !ok {
		msg := fmt.Sprintf("upstream returned invalid response (choices=%v)", data["choices"])
		h.logError(t, publicModel, 200, msg)
		return msg
	}
	if len(choices) == 0 {
		msg := "upstream returned empty choices (likely error)"
		h.logError(t, publicModel, 200, msg)
		return msg
	}
	return ""
}

// ---------- OpenAI 透传 ----------

func (h *Handler) handleOpenAI(w http.ResponseWriter, r *http.Request, body map[string]any, allowed []string, rec *Recorder) {
	modelName, _ := body["model"].(string)

	targets, err := h.balancer.SelectTarget(modelName, allowed)
	if err != nil {
		slog.Error("选择目标失败", "err", err)
		rec.Finish(http.StatusInternalServerError, "select target failed: "+err.Error())
		jsonError(w, http.StatusInternalServerError, "Internal Server Error", "server_error")
		return
	}
	if len(targets) == 0 {
		rec.Finish(http.StatusServiceUnavailable, "No available channel for model: "+modelName)
		jsonError(w, http.StatusServiceUnavailable, "No available channel for model: "+modelName, "server_error")
		return
	}

	upstreamPath := strings.TrimPrefix(r.URL.Path, "/v1")
	isStream, _ := body["stream"].(bool)

	// 参考 one-api：流式请求注入 stream_options 以获取 token 用量
	if isStream {
		so, _ := body["stream_options"].(map[string]any)
		if so == nil {
			so = map[string]any{}
		}
		so["include_usage"] = true
		body["stream_options"] = so
	}

	resp := h.forward(r.Context(), r, fwdSpec{
		targets:     targets,
		publicModel: modelName,
		body:        body,
		rawQuery:    r.URL.RawQuery,
		rec:         rec,
		buildPath: func(t routing.Target) string {
			if strings.Contains(upstreamPath, "/chat/completions") {
				return channel.ChatPath(t.Channel)
			}
			return upstreamPath
		},
		applyModel: func(t routing.Target) {
			if t.UpstreamModel != "" {
				body["model"] = t.UpstreamModel
			}
		},
		isStream:           isStream,
		acceptStreamHeader: false,
		handleUpstream: func(resp *http.Response, t routing.Target) (*http.Response, bool, string) {
			return h.openaiUpstreamResponse(resp, t, upstreamPath, isStream, rec)
		},
		allFailed: func(lastErr, last429 string) *http.Response {
			rec.Finish(http.StatusBadGateway, lastErr)
			return jsonResponse(http.StatusBadGateway, map[string]any{
				"error": map[string]any{
					"message": "All targets failed. Last error: " + lastErr + rateDetail(last429),
					"type":    "server_error",
				},
			})
		},
	})

	if resp == nil {
		rec.Finish(http.StatusBadGateway, "all targets failed")
		rec.Commit()
		writeResponse(w, resp)
		return
	}
	rec.Finish(resp.StatusCode, "")
	resp.Body = &observeBody{ReadCloser: resp.Body, rec: rec}
	writeResponse(w, resp)
}

func (h *Handler) openaiUpstreamResponse(up *http.Response, t routing.Target, upstreamPath string, isStream bool, rec *Recorder) (*http.Response, bool, string) {
	ct := up.Header.Get("Content-Type")

	// 2xx 之外（非 404/429）的响应原样透传给客户端，同时记日志
	if up.StatusCode < 200 || up.StatusCode >= 300 {
		raw := readBodyLimited(up, 300)
		h.logError(t, t.PublicModel, up.StatusCode, raw)
		rec.Finish(up.StatusCode, raw)
		return bytesResponse(up.StatusCode, up.Header, []byte(raw)), false, ""
	}

	headers := http.Header{}
	if ct != "" {
		headers.Set("Content-Type", ct)
	}
	headers.Set("Access-Control-Allow-Origin", "*")

	upstreamIsSSE := isStream && strings.Contains(ct, "text/event-stream")
	if upstreamIsSSE {
		// 先看一眼首包：上游可能用 200 + SSE 把错误当事件发出来，
		// 不探测就会把错误透传给客户端、白白跳过故障转移。
		br := bufio.NewReaderSize(up.Body, 32*1024)
		prefix, failure := peekSSEFailure(br)
		if failure != "" {
			h.logError(t, t.PublicModel, 200, "HTTP 200 stream error: "+failure)
			return nil, true, failure
		}

		headers.Set("Content-Type", "text/event-stream")
		headers.Set("Cache-Control", "no-cache")
		headers.Set("Connection", "keep-alive")
		// 禁用中间代理缓冲，确保实时转发
		headers.Set("X-Accel-Buffering", "no")

		stream := RewriteSSE(replayAfterPeek(prefix, br, up.Body), "chatcmpl-"+id.Hex(12), func(msg string) {
			h.logError(t, t.PublicModel, 200, "HTTP 200 stream error: "+msg)
		}, rec.ObserveEvent, rec.Commit)
		return &http.Response{StatusCode: up.StatusCode, Header: headers, Body: stream}, false, ""
	}

	if isStream {
		slog.Warn("[proxy] 上游对 stream:true 返回了非 SSE 响应，回退到非流式校验", "content_type", ct)
	}

	// 非流式：校验 chat/completions 的 200 响应体
	if up.StatusCode == http.StatusOK && strings.Contains(upstreamPath, "/chat/completions") {
		raw, err := io.ReadAll(up.Body)
		_ = up.Body.Close()
		if err == nil {
			if msg := h.validateUpstreamResult(raw, t, t.PublicModel); msg != "" {
				return nil, true, msg
			}
			rec.ObserveJSON(raw)
			return bytesResponse(200, headers, raw), false, ""
		}
	}

	return &http.Response{StatusCode: up.StatusCode, Header: headers, Body: up.Body}, false, ""
}

// ---------- 模型列表 ----------

func (h *Handler) handleModels(w http.ResponseWriter, allowed []string) {
	channels, err := h.store.ListChannels()
	if err != nil {
		slog.Error("读取渠道失败", "err", err)
		jsonError(w, http.StatusInternalServerError, "Internal Server Error", "server_error")
		return
	}

	var allowedSet map[string]bool
	if len(allowed) > 0 {
		allowedSet = make(map[string]bool, len(allowed))
		for _, cid := range allowed {
			allowedSet[cid] = true
		}
	}

	type item struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int    `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	data := []item{}
	seen := map[string]bool{}

	add := func(name, owner string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		data = append(data, item{ID: name, Object: "model", Created: 0, OwnedBy: owner})
	}

	for i := range channels {
		ch := &channels[i]
		if !ch.Enabled || len(ch.EnabledKeys()) == 0 {
			continue
		}
		if allowedSet != nil && !allowedSet[ch.ID] {
			continue
		}
		owner := ch.Name
		if owner == "" {
			owner = ch.ID
		}
		for pub := range ch.ModelMap {
			add(strings.TrimSpace(pub), owner)
		}
		for _, m := range ch.Models {
			add(strings.TrimSpace(m), owner)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// ---------- 错误日志 ----------

func (h *Handler) logError(t routing.Target, publicModel string, status int, message string) {
	ch := t.Channel
	e := &model.ErrorLog{
		ChannelID:     ch.ID,
		ChannelName:   ch.Name,
		BaseURL:       ch.BaseURL,
		Model:         publicModel,
		UpstreamModel: t.UpstreamModel,
		Status:        status,
		KeyHint:       model.KeyHint(t.Key),
		Message:       truncate(message, 2000),
	}
	// 异步写入，失败不影响转发
	go func() {
		if err := h.store.InsertErrorLog(e); err != nil {
			slog.Error("[errorlog] 写入失败", "err", err)
		}
	}()
}

// ---------- 工具 ----------

func decodeJSONObject(r *http.Request) (map[string]any, error) {
	defer func() { _ = r.Body.Close() }()
	dec := json.NewDecoder(io.LimitReader(r.Body, 32<<20))
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("empty body")
	}
	return out, nil
}

func jsonError(w http.ResponseWriter, status int, message, typ string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": message, "type": typ},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// jsonResponse 构造内存中的 JSON 响应对象。
func jsonResponse(status int, v any) *http.Response {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(`{"error":{"message":"encode failed"}}`)
		status = http.StatusInternalServerError
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Access-Control-Allow-Origin", "*")
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(b))}
}

func bytesResponse(status int, header http.Header, body []byte) *http.Response {
	h := http.Header{}
	for k, vs := range header {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
	h.Set("Access-Control-Allow-Origin", "*")
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(body))}
}

// writeResponse 把上游响应写回客户端；流式响应逐块 flush。
func writeResponse(w http.ResponseWriter, resp *http.Response) {
	if resp == nil {
		jsonError(w, http.StatusBadGateway, "All targets failed", "server_error")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	dst := w.Header()
	for k, vs := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func isHopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-connection", "transfer-encoding",
		"te", "trailer", "upgrade":
		return true
	}
	return false
}

func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

// readBodyLimited 读取并关闭响应体，最多返回 n 字节。
func readBodyLimited(resp *http.Response, n int) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, int64(n)))
	_ = resp.Body.Close()
	return string(b)
}

func rateDetail(last429 string) string {
	if last429 == "" {
		return ""
	}
	if len(last429) > 200 {
		last429 = last429[:200]
	}
	return " | upstream: " + last429
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}