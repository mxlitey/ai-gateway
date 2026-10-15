package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/channel"
	"github.com/czg95/ai-gateway-v2/internal/config"
	"github.com/czg95/ai-gateway-v2/internal/id"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/netinfo"
	"github.com/czg95/ai-gateway-v2/internal/plugin"
	"github.com/czg95/ai-gateway-v2/internal/pricing"
	"github.com/czg95/ai-gateway-v2/internal/proxy"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// API 管理接口处理器。
type API struct {
	store  *store.Store
	svc    *Service
	client *http.Client
	host   *plugin.Host
	pricer *pricing.Resolver
	cfg    *config.Config
}

// NewAPI 创建管理接口处理器。host / pricer 可为 nil；cfg 提供监听地址与数据目录等运行信息。
func NewAPI(s *store.Store, host *plugin.Host, pricer *pricing.Resolver, cfg *config.Config) *API {
	return &API{
		store:  s,
		svc:    New(s),
		client: &http.Client{Timeout: 60 * time.Second},
		host:   host,
		pricer: pricer,
		cfg:    cfg,
	}
}

// Service 暴露管理端服务（供路由层判断初始化状态）。
func (a *API) Service() *Service { return a.svc }

// Handle 处理 /admin/api/* 请求（path 已去掉 /admin/api 前缀）。
func (a *API) Handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/api")
	method := r.Method

	// ---- 无需鉴权 ----
	switch {
	case path == "/setup" && method == http.MethodGet:
		initialized, err := a.svc.IsInitialized()
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, map[string]any{"initialized": initialized})
		return

	case path == "/setup" && method == http.MethodPost:
		var body struct {
			Password string `json:"password"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if err := a.svc.Setup(body.Password); err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "已初始化") {
				status = http.StatusConflict
			}
			jsonErr(w, status, err.Error())
			return
		}
		token, ok, err := a.svc.Login(body.Password)
		if err != nil || !ok {
			jsonOK(w, map[string]any{"success": true})
			return
		}
		jsonOK(w, map[string]any{"token": token})
		return

	case path == "/login" && method == http.MethodPost:
		var body struct {
			Password string `json:"password"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Password) == "" {
			jsonErr(w, http.StatusBadRequest, "Password is required")
			return
		}
		token, ok, err := a.svc.Login(body.Password)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			jsonErr(w, http.StatusUnauthorized, "Invalid password")
			return
		}
		jsonOK(w, map[string]any{"token": token})
		return
	}

	// ---- 以下均需鉴权 ----
	if !a.svc.RequireAuth(r) {
		jsonErr(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	switch {
	case path == "/me" && method == http.MethodGet:
		jsonOK(w, map[string]any{"role": "admin"})

	case path == "/runtime" && method == http.MethodGet:
		a.runtimeInfo(w)

	case path == "/password" && method == http.MethodPost:
		a.changePassword(w, r)

	// ---- 渠道 ----
	case path == "/channels" && method == http.MethodGet:
		a.listChannels(w)
	case path == "/channels" && method == http.MethodPost:
		a.createChannel(w, r)
	case strings.HasPrefix(path, "/channels/"):
		a.channelSubroutes(w, r, path, method)

	// ---- 客户端密钥 ----
	case path == "/apikeys" && method == http.MethodGet:
		a.listAPIKeys(w)
	case path == "/apikeys" && method == http.MethodPost:
		a.createAPIKey(w, r)
	case strings.HasPrefix(path, "/apikeys/"):
		a.apiKeySubroutes(w, r, path, method)

	// ---- 错误日志 ----
	case path == "/errors" && method == http.MethodGet:
		a.listErrors(w, r)

	// ---- 上游辅助 ----
	case path == "/fetch-models" && method == http.MethodPost:
		a.fetchModels(w, r)
	case path == "/test-upstream" && method == http.MethodPost:
		a.testUpstream(w, r)
	case path == "/public-models" && method == http.MethodGet:
		a.publicModels(w)

	// ---- 插件 ----
	case path == "/plugins" && method == http.MethodGet:
		a.listPlugins(w)
	case path == "/plugins/rescan" && method == http.MethodPost:
		a.rescanPlugins(w)
	case strings.HasPrefix(path, "/plugins/"):
		a.pluginSubroutes(w, r, path, method)

	// ---- 用量与费用 ----
	case path == "/usage/summary" && method == http.MethodGet:
		a.usageSummary(w, r)
	case path == "/usage/logs" && method == http.MethodGet:
		a.usageLogs(w, r)
	case path == "/usage/series" && method == http.MethodGet:
		a.usageSeries(w, r)
	case path == "/usage" && method == http.MethodDelete:
		days, err := parseDaysParam(r.URL.Query().Get("days"), manualCleanupDays)
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		logs, errs, err := a.cleanupUsage(days)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, map[string]any{
			"success":        true,
			"removed":        logs,
			"removed_errors": errs,
			"days":           days,
		})

	// ---- 价格表 ----
	case path == "/prices" && method == http.MethodGet:
		a.listPrices(w)
	case path == "/prices" && method == http.MethodPut:
		a.upsertPrice(w, r)
	case path == "/prices/refresh" && method == http.MethodPost:
		a.refreshPrices(w)
	case path == "/prices" && method == http.MethodDelete:
		a.deletePrice(w, r)

	default:
		jsonErr(w, http.StatusNotFound, "Not found")
	}
}

// ---------- 渠道 ----------

func (a *API) listChannels(w http.ResponseWriter) {
	channels, err := a.store.ListChannels()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]model.Channel, 0, len(channels))
	for i := range channels {
		out = append(out, maskChannel(channels[i]))
	}
	jsonOK(w, out)
}

func (a *API) createChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if !decodeBody(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.BaseURL) == "" {
		jsonErr(w, http.StatusBadRequest, "name and base_url are required")
		return
	}
	order, err := a.store.NextChannelSortOrder()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ch := &model.Channel{
		ID:            id.UUID(),
		Name:          strings.TrimSpace(in.Name),
		BaseURL:       strings.TrimSpace(in.BaseURL),
		Path:          strings.TrimSpace(in.Path),
		Keys:          sanitizeKeys(in.Keys),
		Models:        sanitizeStrings(in.Models),
		ModelMap:      normalizeModelMap(in.ModelMap),
		ModelPriority: map[string]int{},
		Headers:       channel.NormalizeHeaders(in.Headers),
		KeyStrategy:   model.NormalizeKeyStrategy(in.KeyStrategy),
		Enabled:       in.Enabled == nil || *in.Enabled,
		Source:        "manual",
		SortOrder:     order,
		CreatedAt:     time.Now().UnixMilli(),
	}
	if err := a.store.CreateChannel(ch); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, maskChannel(*ch))
}

func (a *API) channelSubroutes(w http.ResponseWriter, r *http.Request, path, method string) {
	rest := strings.TrimPrefix(path, "/channels/")

	// /channels/:id/toggle
	if strings.HasSuffix(rest, "/toggle") && method == http.MethodPatch {
		a.toggleChannel(w, strings.TrimSuffix(rest, "/toggle"))
		return
	}
	// /channels/:id/priority
	if strings.HasSuffix(rest, "/priority") && method == http.MethodPatch {
		a.setChannelPriority(w, r, strings.TrimSuffix(rest, "/priority"))
		return
	}
	if strings.Contains(rest, "/") {
		jsonErr(w, http.StatusNotFound, "Not found")
		return
	}

	switch method {
	case http.MethodPut:
		a.updateChannel(w, r, rest)
	case http.MethodDelete:
		ch, err := a.store.GetChannel(rest)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ch == nil {
			jsonErr(w, http.StatusNotFound, "Channel not found")
			return
		}
		if ch.IsPluginChannel() {
			jsonErr(w, http.StatusConflict, "该渠道由插件注册，无法删除")
			return
		}
		if err := a.store.DeleteChannel(rest); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, map[string]any{"success": true})
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (a *API) updateChannel(w http.ResponseWriter, r *http.Request, channelID string) {
	existing, err := a.store.GetChannel(channelID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		jsonErr(w, http.StatusNotFound, "Channel not found")
		return
	}

	var in channelInput
	if !decodeBody(w, r, &in) {
		return
	}

	next := *existing
	if s := strings.TrimSpace(in.Name); s != "" {
		next.Name = s
	}
	if s := strings.TrimSpace(in.BaseURL); s != "" {
		next.BaseURL = s
	}
	next.Path = strings.TrimSpace(in.Path)
	if in.Keys != nil {
		next.Keys = mergeKeys(existing.Keys, in.Keys)
	}
	if in.Models != nil {
		next.Models = sanitizeStrings(in.Models)
	}
	if in.ModelMap != nil {
		next.ModelMap = normalizeModelMap(in.ModelMap)
	}
	if in.Headers != nil {
		next.Headers = channel.NormalizeHeaders(in.Headers)
	}
	if s := strings.TrimSpace(in.KeyStrategy); s != "" {
		next.KeyStrategy = model.NormalizeKeyStrategy(s)
	}
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}

	if err := a.store.UpdateChannel(&next); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, maskChannel(next))
}

func (a *API) toggleChannel(w http.ResponseWriter, channelID string) {
	ch, err := a.store.GetChannel(channelID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ch == nil {
		jsonErr(w, http.StatusNotFound, "Channel not found")
		return
	}
	ch.Enabled = !ch.Enabled
	if err := a.store.UpdateChannel(ch); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, maskChannel(*ch))
}

func (a *API) setChannelPriority(w http.ResponseWriter, r *http.Request, channelID string) {
	var body struct {
		Model    string `json:"model"`
		Priority int    `json:"priority"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	mdl := strings.TrimSpace(body.Model)
	if mdl == "" {
		jsonErr(w, http.StatusBadRequest, "model and numeric priority are required")
		return
	}
	ch, err := a.store.GetChannel(channelID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ch == nil {
		jsonErr(w, http.StatusNotFound, "Channel not found")
		return
	}
	if ch.ModelPriority == nil {
		ch.ModelPriority = map[string]int{}
	}
	priority := body.Priority
	if priority < 0 {
		priority = 0
	}
	if priority > 9999 {
		priority = 9999
	}
	if priority == 0 {
		delete(ch.ModelPriority, mdl)
	} else {
		ch.ModelPriority[mdl] = priority
	}
	if err := a.store.UpdateChannel(ch); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]any{"success": true, "model": mdl, "priority": priority})
}

// ---------- 客户端密钥 ----------

// listAPIKeys 返回完整密钥：管理面板是本机密码保护的服务，
// 密钥需要在列表里随时复制（与旧版 ai-gateway 一致），脱敏只在前端展示层做。
func (a *API) listAPIKeys(w http.ResponseWriter) {
	keys, err := a.store.ListAPIKeys()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []model.APIKey{}
	}
	jsonOK(w, keys)
}

func (a *API) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string   `json:"name"`
		ChannelIDs []string `json:"channel_ids"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Unnamed"
	}
	k := &model.APIKey{
		ID:         id.UUID(),
		Name:       name,
		Key:        id.APIKey(),
		ChannelIDs: sanitizeStrings(body.ChannelIDs),
		Enabled:    true,
		CreatedAt:  time.Now().UnixMilli(),
	}
	if err := a.store.CreateAPIKey(k); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, k) // 创建时返回完整密钥（仅此一次）
}

func (a *API) apiKeySubroutes(w http.ResponseWriter, r *http.Request, path, method string) {
	keyID := strings.TrimPrefix(path, "/apikeys/")
	if keyID == "" || strings.Contains(keyID, "/") {
		jsonErr(w, http.StatusNotFound, "Not found")
		return
	}

	switch method {
	case http.MethodDelete:
		if _, err := a.store.GetAPIKey(keyID); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := a.store.DeleteAPIKey(keyID); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, map[string]any{"success": true})

	case http.MethodPatch:
		k, err := a.store.GetAPIKey(keyID)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if k == nil {
			jsonErr(w, http.StatusNotFound, "API key not found")
			return
		}
		var body struct {
			Name       *string   `json:"name"`
			Enabled    *bool     `json:"enabled"`
			ChannelIDs *[]string `json:"channel_ids"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
			k.Name = strings.TrimSpace(*body.Name)
		}
		if body.Enabled != nil {
			k.Enabled = *body.Enabled
		}
		if body.ChannelIDs != nil {
			k.ChannelIDs = sanitizeStrings(*body.ChannelIDs)
		}
		if err := a.store.UpdateAPIKey(k); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		k.Key = model.MaskKey(k.Key)
		jsonOK(w, k)

	default:
		jsonErr(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// ---------- 管理员密码 ----------

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	encoded, ok, err := a.store.GetSetting(store.SettingAdminPasswordHash)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok || !VerifyPassword(body.OldPassword, encoded) {
		jsonErr(w, http.StatusUnauthorized, "当前密码不正确")
		return
	}
	if err := a.svc.SetPassword(body.NewPassword); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]any{"success": true})
}

// ---------- 错误日志 ----------

func (a *API) listErrors(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().In(beijingLocation()).Format("2006-01-02")
	}
	from, to, err := beijingDayRange(date)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "date 需为 YYYY-MM-DD")
		return
	}

	channels, err := a.store.ListChannels()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	type channelErrors struct {
		ChannelID   string           `json:"channel_id"`
		ChannelName string           `json:"channel_name"`
		Errors      []model.ErrorLog `json:"errors"`
	}
	result := []channelErrors{}
	for i := range channels {
		ch := &channels[i]
		logs, err := a.store.ListErrorLogsByChannel(ch.ID, from, to)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(logs) > 0 {
			result = append(result, channelErrors{
				ChannelID:   ch.ID,
				ChannelName: ch.Name,
				Errors:      logs,
			})
		}
	}
	jsonOK(w, map[string]any{"date": date, "channels": result})
}

// manualCleanupDays 手动清理阈值：错误日志与用量明细都长期存储，
// 只有用户在用量页显式点击「清理旧记录」时才删除更早的记录（两张表按同一时间点清理）。
const manualCleanupDays = 90

// ---------- 运行信息 ----------

// runtimeInfo 返回网关的监听情况：访问范围决定对外展示用回环地址还是本机局域网 IP。
func (a *API) runtimeInfo(w http.ResponseWriter) {
	cfg := a.cfg
	if cfg == nil {
		jsonErr(w, http.StatusInternalServerError, "运行配置不可用")
		return
	}
	lan := !netinfo.IsLoopbackHost(cfg.Host)
	host := "127.0.0.1"
	if lan {
		if ip := netinfo.PrimaryIPv4(); ip != "" {
			host = ip
		}
	}
	base := fmt.Sprintf("http://%s:%d", host, cfg.Port)
	jsonOK(w, map[string]any{
		"lan":         lan,
		"listen_host": cfg.Host,
		"port":        cfg.Port,
		"access_url":  base,
		"api_url":     base + "/v1",
		"data_dir":    cfg.DataDir,
	})
}

// ---------- 上游辅助 ----------

func (a *API) fetchModels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChannelID string   `json:"channel_id"`
		BaseURL   string   `json:"base_url"`
		Keys      []string `json:"keys"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	var ch *model.Channel
	if body.ChannelID != "" {
		var err error
		ch, err = a.store.GetChannel(body.ChannelID)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ch == nil {
			jsonErr(w, http.StatusNotFound, "Channel not found")
			return
		}
	} else {
		ch = &model.Channel{}
	}

	if base := strings.TrimSpace(body.BaseURL); base != "" {
		ch.BaseURL = base
	}

	if len(body.Keys) > 0 {
		// 前端回传的可能是脱敏值（长度超限的密钥在读取接口里被打码），先还原成原值。
		byMask := map[string]string{}
		for _, k := range ch.Keys {
			byMask[model.MaskKey(k.Key)] = k.Key
		}
		keys := make([]model.ChannelKey, 0, len(body.Keys))
		for _, k := range body.Keys {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if real, ok := byMask[k]; ok {
				k = real
			}
			keys = append(keys, model.ChannelKey{Key: k, Enabled: true})
		}
		ch.Keys = keys
	}

	if channel.BaseURL(ch) == "" || len(ch.EnabledKeys()) == 0 {
		jsonErr(w, http.StatusBadRequest, "基础 URL 和密钥不能为空")
		return
	}

	models, err := a.fetchUpstreamModels(ch)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]any{"models": models})
}

func (a *API) fetchUpstreamModels(ch *model.Channel) ([]string, error) {
	modelsURL := channel.BaseURL(ch) + channel.ModelsPath(ch)
	var lastErr error
	for _, key := range ch.EnabledKeys() {
		req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		headers := http.Header{}
		headers.Set("Content-Type", "application/json")
		channel.ApplyUpstreamKey(headers, ch, key)
		channel.ApplyChannelHeaders(headers, ch, nil)
		req.Header = headers

		resp, err := a.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var data struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(raw, &data); err != nil {
				lastErr = fmt.Errorf("解析上游响应失败")
				continue
			}
			out := make([]string, 0, len(data.Data))
			for _, m := range data.Data {
				if m.ID != "" {
					out = append(out, m.ID)
				}
			}
			return out, nil
		}
		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(raw), 200))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("获取模型失败")
	}
	return nil, lastErr
}

// testTask 一次诊断任务。
type testTask struct {
	ChannelID string `json:"channel_id"`
	Key       string `json:"key"`
	Model     string `json:"model"`
}

// probeResult 单个「渠道 × 密钥 × 模型」的诊断结果。
type probeResult struct {
	Model       string            `json:"model"`
	Channel     string            `json:"channel"`
	ChannelID   string            `json:"channel_id"`
	KeyHint     string            `json:"key_hint"`
	Status      int               `json:"status"`
	DurationMS  int64             `json:"duration_ms"`
	RateHeaders map[string]string `json:"rate_headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	Error       string            `json:"error,omitempty"`
}

func (a *API) testUpstream(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tasks     []testTask `json:"tasks"`
		ChannelID string     `json:"channel_id"`
		Key       string     `json:"key"`
		Model     string     `json:"model"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	tasks := body.Tasks
	if len(tasks) == 0 && (body.Model != "" || body.ChannelID != "" || body.Key != "") {
		tasks = append(tasks, testTask{ChannelID: body.ChannelID, Key: body.Key, Model: body.Model})
	}
	if len(tasks) == 0 {
		jsonErr(w, http.StatusBadRequest, "请指定要诊断的模型或任务")
		return
	}

	channels, err := a.store.ListChannels()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	results := []probeResult{}

	rateHeaderNames := []string{
		"modelscope-ratelimit-requests-limit", "modelscope-ratelimit-requests-remaining",
		"modelscope-ratelimit-model-requests-limit", "modelscope-ratelimit-model-requests-remaining",
		"retry-after", "x-ratelimit-limit-requests", "x-ratelimit-remaining-requests",
	}

	for _, tk := range tasks {
		mdl := strings.TrimSpace(tk.Model)
		for i := range channels {
			ch := &channels[i]
			if tk.ChannelID != "" && ch.ID != tk.ChannelID {
				continue
			}
			if tk.ChannelID == "" && (!ch.Enabled || len(ch.EnabledKeys()) == 0) {
				continue
			}
			keys := ch.EnabledKeys()
			if tk.Key != "" {
				filtered := keys[:0:0]
				for _, k := range keys {
					if k == tk.Key {
						filtered = append(filtered, k)
					}
				}
				keys = filtered
			}
			if len(keys) == 0 {
				continue
			}
			if mdl == "" {
				results = append(results, probeResult{
					Model: "", Channel: ch.Name, ChannelID: ch.ID,
					Error: "未指定模型，无法诊断",
				})
				continue
			}
			upstreamModel, ok := ch.ResolveUpstreamModel(mdl)
			if !ok {
				upstreamModel = mdl
			}
			for _, key := range keys {
				res := a.probeUpstream(ch, key, upstreamModel, mdl, rateHeaderNames)
				results = append(results, res)
			}
		}
	}

	if len(results) == 0 {
		jsonErr(w, http.StatusNotFound, "没有可诊断的目标（渠道/key/模型不匹配）")
		return
	}

	okCount, rateLimited := 0, 0
	for _, res := range results {
		if res.Status == http.StatusOK && res.Error == "" {
			okCount++
		}
		if res.Status == http.StatusTooManyRequests {
			rateLimited++
		}
	}
	jsonOK(w, map[string]any{
		"summary": map[string]any{"total": len(results), "ok": okCount, "rate_limited": rateLimited},
		"results": results,
	})
}

func (a *API) probeUpstream(ch *model.Channel, key, upstreamModel, publicModel string, rateHeaderNames []string) probeResult {
	res := probeResult{
		Model:     publicModel,
		Channel:   ch.Name,
		ChannelID: ch.ID,
		KeyHint:   model.KeyHint(key),
	}

	payload, _ := json.Marshal(map[string]any{
		"model":      upstreamModel,
		"messages":   []any{map[string]any{"role": "user", "content": "say ok"}},
		"max_tokens": 3,
	})
	testURL := channel.BaseURL(ch) + channel.ChatPath(ch)

	req, err := http.NewRequest(http.MethodPost, testURL, bytes.NewReader(payload))
	if err != nil {
		res.Error = err.Error()
		a.logDiagnoseError(ch, key, publicModel, 0, err.Error())
		a.recordDiagnoseUsage(ch, key, publicModel, upstreamModel, 0, 0, nil, err.Error())
		return res
	}
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	channel.ApplyUpstreamKey(headers, ch, key)
	channel.ApplyChannelHeaders(headers, ch, nil)
	req.Header = headers

	start := time.Now()
	resp, err := a.client.Do(req)
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		msg := "network error: " + err.Error()
		a.logDiagnoseError(ch, key, publicModel, 0, msg)
		a.recordDiagnoseUsage(ch, key, publicModel, upstreamModel, 0, res.DurationMS, nil, msg)
		return res
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()

	res.Status = resp.StatusCode
	res.Body = truncateStr(string(raw), 300)

	rate := map[string]string{}
	for _, h := range rateHeaderNames {
		if v := resp.Header.Get(h); v != "" {
			rate[h] = v
		}
	}
	if len(rate) > 0 {
		res.RateHeaders = rate
	}

	// 与真实转发同一判定标准：200 也可能是「内容为错误」的假成功
	failMsg := ""
	if invalid := describeInvalidChatBody(raw); invalid != "" {
		res.Error = invalid
		failMsg = invalid
		a.logDiagnoseError(ch, key, publicModel, 200, "HTTP 200 with error: "+invalid)
	} else if resp.StatusCode != http.StatusOK {
		failMsg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateStr(string(raw), 200))
		a.logDiagnoseError(ch, key, publicModel, resp.StatusCode, failMsg)
	}
	// 诊断会真实消耗上游额度，必须计入用量明细
	a.recordDiagnoseUsage(ch, key, publicModel, upstreamModel, resp.StatusCode, res.DurationMS, raw, failMsg)
	return res
}

// recordDiagnoseUsage 把一次诊断请求写入用量明细，与 /v1 转发口径保持一致。
func (a *API) recordDiagnoseUsage(ch *model.Channel, key, publicModel, upstreamModel string, status int, durationMS int64, raw []byte, errMsg string) {
	row := model.RequestLog{
		Endpoint:        "diagnose",
		ChannelID:       ch.ID,
		ChannelName:     ch.Name,
		Model:           publicModel,
		UpstreamModel:   upstreamModel,
		UpstreamKeyHint: model.KeyHint(key),
		Status:          status,
		Attempts:        1,
		DurationMS:      durationMS,
		PriceSource:     pricing.SourceNone,
		Error:           errMsg,
	}
	proxy.ExtractUsage(&row, raw)
	proxy.RecordUsage(a.store, a.pricer, row)
}

func (a *API) logDiagnoseError(ch *model.Channel, key, publicModel string, status int, message string) {
	e := &model.ErrorLog{
		ChannelID:     ch.ID,
		ChannelName:   ch.Name,
		BaseURL:       ch.BaseURL,
		Model:         publicModel,
		UpstreamModel: publicModel,
		Status:        status,
		KeyHint:       model.KeyHint(key),
		Message:       truncateStr(message, 2000),
	}
	go func() {
		if err := a.store.InsertErrorLog(e); err != nil {
			slog.Error("[errorlog] 诊断日志写入失败", "err", err)
		}
	}()
}

// ---------- 模型路由视图 ----------

func (a *API) publicModels(w http.ResponseWriter) {
	channels, err := a.store.ListChannels()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	type route struct {
		ChannelID   string `json:"channel_id"`
		ChannelName string `json:"channel_name"`
		Enabled     bool   `json:"enabled"`
		Priority    int    `json:"priority"`
		Upstream    string `json:"upstream_model"`
		// Direct 表示该公开模型未做重命名映射、按同名透传到上游。
		Direct bool `json:"direct"`
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
			um, ok := ch.ResolveUpstreamModel(pub)
			if !ok {
				continue
			}
			_, mapped := ch.ModelMap[pub]
			grouped[pub] = append(grouped[pub], route{
				ChannelID:   ch.ID,
				ChannelName: ch.Name,
				Enabled:     ch.Enabled && len(ch.EnabledKeys()) > 0,
				Priority:    ch.Priority(pub),
				Upstream:    um,
				Direct:      !mapped,
			})
		}
	}

	models := make([]string, 0, len(grouped))
	for k := range grouped {
		models = append(models, k)
	}
	sort.Strings(models)

	out := make([]map[string]any, 0, len(models))
	for _, m := range models {
		routes := grouped[m]
		sort.SliceStable(routes, func(i, j int) bool { return routes[i].Priority > routes[j].Priority })
		out = append(out, map[string]any{"model": m, "routes": routes})
	}
	jsonOK(w, out)
}

// ---------- 插件 ----------

func (a *API) listPlugins(w http.ResponseWriter) {
	if a.host == nil {
		jsonOK(w, []any{})
		return
	}
	jsonOK(w, a.host.List())
}

func (a *API) rescanPlugins(w http.ResponseWriter) {
	if a.host == nil {
		jsonErr(w, http.StatusNotImplemented, "插件宿主未启用")
		return
	}
	if err := a.host.Discover(); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, a.host.List())
}

func (a *API) pluginSubroutes(w http.ResponseWriter, r *http.Request, path, method string) {
	rest := strings.TrimPrefix(path, "/plugins/")

	// 控制台反向代理：/plugins/<id>/console[/<子路径>]
	if i := strings.Index(rest, "/console"); i > 0 {
		pluginID := rest[:i]
		sub := strings.TrimPrefix(rest[i+len("/console"):], "/")
		a.pluginConsole(w, r, pluginID, sub)
		return
	}

	switch {
	case strings.HasSuffix(rest, "/start") && method == http.MethodPost:
		a.pluginAction(w, strings.TrimSuffix(rest, "/start"), "start")
	case strings.HasSuffix(rest, "/stop") && method == http.MethodPost:
		a.pluginAction(w, strings.TrimSuffix(rest, "/stop"), "stop")
	case strings.HasSuffix(rest, "/restart") && method == http.MethodPost:
		a.pluginAction(w, strings.TrimSuffix(rest, "/restart"), "restart")
	case !strings.Contains(rest, "/") && rest != "" && method == http.MethodPatch:
		a.updatePlugin(w, r, rest)
	case !strings.Contains(rest, "/") && rest != "" && method == http.MethodDelete:
		a.removePlugin(w, rest)
	case !strings.Contains(rest, "/") && rest != "":
		jsonErr(w, http.StatusMethodNotAllowed, "Method not allowed")
	default:
		jsonErr(w, http.StatusNotFound, "Not found")
	}
}

func (a *API) pluginAction(w http.ResponseWriter, pluginID, action string) {
	if a.host == nil {
		jsonErr(w, http.StatusNotImplemented, "插件宿主未启用")
		return
	}
	if pluginID == "" {
		jsonErr(w, http.StatusBadRequest, "缺少插件 id")
		return
	}

	var err error
	switch action {
	case "start":
		err = a.host.Start(pluginID)
	case "stop":
		err = a.host.Stop(pluginID)
	case "restart":
		err = a.host.Restart(pluginID)
	}
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]any{"success": true})
}

func (a *API) updatePlugin(w http.ResponseWriter, r *http.Request, pluginID string) {
	if a.host == nil {
		jsonErr(w, http.StatusNotImplemented, "插件宿主未启用")
		return
	}
	var body struct {
		Enabled   *bool `json:"enabled"`
		Autostart *bool `json:"autostart"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Enabled == nil && body.Autostart == nil {
		jsonErr(w, http.StatusBadRequest, "请提供 enabled 或 autostart")
		return
	}
	if err := a.host.SetFlags(pluginID, body.Enabled, body.Autostart); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]any{"success": true})
}

// removePlugin 卸载插件：删除磁盘目录与记录。
func (a *API) removePlugin(w http.ResponseWriter, pluginID string) {
	if a.host == nil {
		jsonErr(w, http.StatusNotImplemented, "插件宿主未启用")
		return
	}
	if err := a.host.Remove(pluginID); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]any{"success": true})
}

// pluginConsole 把面板请求反向代理到插件进程内声明的控制台接口。
// 插件只需按约定实现 REST 接口，界面由面板统一渲染（见 docs/plugin-spec.md §6）。
func (a *API) pluginConsole(w http.ResponseWriter, r *http.Request, pluginID, sub string) {
	if a.host == nil {
		jsonErr(w, http.StatusNotImplemented, "插件宿主未启用")
		return
	}
	base, err := a.host.ConsoleTarget(pluginID)
	if err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}

	target := base
	if sub != "" {
		target += "/" + sub
	}
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}

	var body io.Reader
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = r.Body.Close()
		if err != nil {
			jsonErr(w, http.StatusBadRequest, "读取请求体失败")
			return
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "调用插件控制台失败: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 8<<20))

	// 账号增删或登录完成后，插件渠道的密钥要跟着刷新（轮询/故障转移的对象就是账号）
	if resp.StatusCode < 300 && pluginAccountsChanged(r.Method, sub) {
		if err := a.host.SyncChannels(pluginID); err != nil {
			slog.Warn("[plugin] 同步插件账号到渠道密钥失败", "plugin", pluginID, "err", err)
		}
	}
}

// pluginAccountsChanged 判断这次控制台调用是否可能改变账号列表。
func pluginAccountsChanged(method, sub string) bool {
	if strings.HasPrefix(sub, "login/") {
		return true // 登录轮询：授权完成时插件会写入新账号
	}
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	return sub == "accounts" || strings.HasPrefix(sub, "accounts/")
}

// ---------- 用量与费用 ----------

// 趋势图一次能拉取的时间桶上限（按天 / 按小时），避免日期范围选得过大
const (
	maxDayBuckets  = 366
	maxHourBuckets = 720
)

func (a *API) usageSummary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days := clampDays(q.Get("days"), 7)
	to := time.Now().UnixMilli()
	from := to - int64(days)*24*60*60*1000

	// 日历选择器给出的时间范围（毫秒，[from, to)）优先；未给出时再看单日 date
	if f, t, ok := rangeParams(q.Get("from"), q.Get("to")); ok {
		from, to = f, t
	} else if date := strings.TrimSpace(q.Get("date")); date != "" {
		// 指定了日期（YYYY-MM-DD，北京时区）时按该自然日统计
		start, end, err := beijingDayRange(date)
		if err != nil {
			jsonErr(w, http.StatusBadRequest, "date 需为 YYYY-MM-DD")
			return
		}
		from, to = start, end
	}

	// 合计数字跟随渠道名 / 模型 / 上游密钥筛选；分组结果（by_*）只按时间区间统计，用于填充筛选下拉框
	sum, err := a.store.UsageSummary(from, to,
		strings.TrimSpace(q.Get("channel_name")), strings.TrimSpace(q.Get("model")), strings.TrimSpace(q.Get("key_hint")))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	priceCount, err := a.store.CountModelPrices()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	refreshedAt := ""
	if v, ok, _ := a.store.GetSetting(store.SettingPriceRefreshAt); ok {
		refreshedAt = v
	}
	jsonOK(w, map[string]any{
		"days":         days,
		"summary":      sum,
		"price_count":  priceCount,
		"price_source": pricing.Endpoint(),
		"refreshed_at": refreshedAt,
	})
}

func (a *API) usageLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days := clampDays(q.Get("days"), 7)

	to := time.Now().UnixMilli()
	if v := atoi64Or(q.Get("to"), 0); v > 0 {
		to = v
	}
	from := to - int64(days)*24*60*60*1000
	if v := atoi64Or(q.Get("from"), 0); v > 0 {
		from = v
	}

	f := store.RequestLogFilter{
		FromMs:      from,
		ToMs:        to,
		Model:       strings.TrimSpace(q.Get("model")),
		ChannelID:   strings.TrimSpace(q.Get("channel_id")),
		ChannelName: strings.TrimSpace(q.Get("channel_name")),
		APIKeyID:    strings.TrimSpace(q.Get("api_key_id")),
		KeyHint:     strings.TrimSpace(q.Get("key_hint")),
		OnlyFail:    q.Get("only_fail") == "1" || q.Get("only_fail") == "true",
		Limit:       atoiOr(q.Get("limit"), 50),
		Offset:      atoiOr(q.Get("offset"), 0),
	}

	items, err := a.store.ListRequestLogs(f)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := a.store.CountRequestLogs(f)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  f.Limit,
		"offset": f.Offset,
		"from":   from,
		"to":     to,
	})
}

// usageSeries 按小时 / 天聚合用量，供「用量趋势」柱状图使用。
// 支持按渠道名（与按渠道分组的口径一致）与公开模型名筛选；时间范围可由日历选择器
// 以 from/to（毫秒，[from, to)）给出，否则退回「最近 N 个小时 / 天」的固定窗口。
func (a *API) usageSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	bucket := strings.TrimSpace(q.Get("bucket"))
	if bucket != store.UsageBucketHour {
		bucket = store.UsageBucketDay
	}

	channelName := strings.TrimSpace(q.Get("channel_name"))
	modelName := strings.TrimSpace(q.Get("model"))
	keyHint := strings.TrimSpace(q.Get("key_hint"))

	if from, to, ok := rangeParams(q.Get("from"), q.Get("to")); ok {
		step := int64(24 * 60 * 60 * 1000)
		limit, unit := maxDayBuckets, "天"
		if bucket == store.UsageBucketHour {
			step = 60 * 60 * 1000
			limit, unit = maxHourBuckets, "小时"
		}
		if n := int((to-from)/step) + 2; n > limit {
			jsonErr(w, http.StatusBadRequest,
				fmt.Sprintf("时间范围过大：按%s最多查看 %d %s", unit, limit, unit))
			return
		}

		points, from, to, err := a.store.UsageSeriesRange(from, to, bucket, channelName, modelName, keyHint)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, map[string]any{"bucket": bucket, "from": from, "to": to, "points": points})
		return
	}

	// 按小时固定看最近 24 个小时桶；按天则与页面上选择的天数一致（含当天）
	count := clampDays(q.Get("days"), 7)
	if bucket == store.UsageBucketHour {
		count = 24
	}

	points, from, to, err := a.store.UsageSeries(time.Now().UnixMilli(), count, bucket, channelName, modelName, keyHint)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]any{"bucket": bucket, "from": from, "to": to, "points": points})
}

// rangeParams 解析日历选择器给出的毫秒时间范围 [from, to)：两者都合法才生效。
func rangeParams(fromStr, toStr string) (int64, int64, bool) {
	from := atoi64Or(fromStr, 0)
	to := atoi64Or(toStr, 0)
	if from <= 0 || to <= 0 || to <= from {
		return 0, 0, false
	}
	return from, to, true
}

// cleanupUsage 清理 N 天前的用量明细；days 为 0 时连当天一起清掉。
func (a *API) cleanupUsage(days int) (logs, errs int64, err error) {
	before := time.Now().AddDate(0, 0, -days).UnixMilli()
	if logs, err = a.store.CleanupRequestLogsBefore(before); err != nil {
		return 0, 0, err
	}
	// 错误日志与用量明细配套（「今日错误分布」读的是 error_logs），按同一时间点一起清理
	errs, err = a.store.CleanupErrorLogsBefore(before)
	return logs, errs, err
}

// parseDaysParam 解析「保留最近 N 天」的查询参数；缺省用 def，0 表示不保留（含当天一起清理）。
func parseDaysParam(raw string, def int) (int, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 3650 {
		return 0, fmt.Errorf("days 需为 0-3650 之间的整数")
	}
	return n, nil
}

// ---------- 价格表 ----------

func (a *API) listPrices(w http.ResponseWriter) {
	items, err := a.store.ListModelPrices()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	refreshedAt := ""
	if v, ok, _ := a.store.GetSetting(store.SettingPriceRefreshAt); ok {
		refreshedAt = v
	}
	jsonOK(w, map[string]any{
		"items":        items,
		"count":        len(items),
		"endpoint":     pricing.Endpoint(),
		"refreshed_at": refreshedAt,
		"unit":         "USD / 百万 token",
	})
}

func (a *API) refreshPrices(w http.ResponseWriter) {
	if a.pricer == nil {
		jsonErr(w, http.StatusNotImplemented, "价格模块未启用")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	n, err := a.pricer.Refresh(ctx, a.client, pricing.Endpoint())
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	jsonOK(w, map[string]any{"success": true, "models": n})
}

func (a *API) upsertPrice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model           string  `json:"model"`
		DisplayName     string  `json:"display_name"`
		PromptPrice     float64 `json:"prompt_price"`
		CompletionPrice float64 `json:"completion_price"`
		CacheReadPrice  float64 `json:"cache_read_price"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	key := pricing.Normalize(body.Model)
	if key == "" {
		jsonErr(w, http.StatusBadRequest, "model 不能为空")
		return
	}
	if body.PromptPrice < 0 || body.CompletionPrice < 0 || body.CacheReadPrice < 0 {
		jsonErr(w, http.StatusBadRequest, "价格不能为负数")
		return
	}

	p := &model.ModelPrice{
		Model:           key,
		DisplayName:     strings.TrimSpace(body.DisplayName),
		PromptPrice:     body.PromptPrice,
		CompletionPrice: body.CompletionPrice,
		CacheReadPrice:  body.CacheReadPrice,
		Source:          pricing.SourceManual,
	}
	if err := a.store.UpsertModelPrice(p); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.reloadPrices()
	jsonOK(w, p)
}

func (a *API) deletePrice(w http.ResponseWriter, r *http.Request) {
	// 模型名可能带厂商前缀（anthropic/claude-x），用查询参数传递，避免路径转义歧义
	name := pricing.Normalize(r.URL.Query().Get("model"))
	if name == "" {
		jsonErr(w, http.StatusBadRequest, "缺少 model 参数")
		return
	}
	if err := a.store.DeleteModelPrice(name); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.reloadPrices()
	jsonOK(w, map[string]any{"success": true, "model": name})
}

func (a *API) reloadPrices() {
	if a.pricer == nil {
		return
	}
	if err := a.pricer.Reload(); err != nil {
		slog.Error("[pricing] 重建价格索引失败", "err", err)
	}
}

// ---------- 输入模型与工具 ----------

type channelInput struct {
	Name        string             `json:"name"`
	BaseURL     string             `json:"base_url"`
	Path        string             `json:"path"`
	Keys        []model.ChannelKey `json:"keys"`
	Models      []string           `json:"models"`
	ModelMap    map[string]string  `json:"model_map"`
	Headers     []model.Header     `json:"headers"`
	KeyStrategy string             `json:"key_strategy"`
	Enabled     *bool              `json:"enabled"`
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer func() { _ = r.Body.Close() }()
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	if err := dec.Decode(dst); err != nil {
		jsonErr(w, http.StatusBadRequest, "Invalid JSON body")
		return false
	}
	return true
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func jsonErr(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"error": message})
}

// maskChannel 对渠道密钥做脱敏后返回。
func maskChannel(ch model.Channel) model.Channel {
	if ch.Keys == nil {
		ch.Keys = []model.ChannelKey{}
	}
	for i := range ch.Keys {
		ch.Keys[i].Key = model.MaskKey(ch.Keys[i].Key)
	}
	return ch
}

// mergeKeys 合并提交的密钥：等于脱敏值时保留原值。
func mergeKeys(existing, incoming []model.ChannelKey) []model.ChannelKey {
	byMask := map[string]string{}
	for _, k := range existing {
		byMask[model.MaskKey(k.Key)] = k.Key
	}
	out := make([]model.ChannelKey, 0, len(incoming))
	for _, k := range incoming {
		v := strings.TrimSpace(k.Key)
		if strings.Contains(v, "...") {
			if real, ok := byMask[v]; ok {
				v = real
			}
		}
		if v == "" {
			continue
		}
		out = append(out, model.ChannelKey{Key: v, Enabled: k.Enabled, Label: strings.TrimSpace(k.Label)})
	}
	return out
}

func sanitizeKeys(keys []model.ChannelKey) []model.ChannelKey {
	out := make([]model.ChannelKey, 0, len(keys))
	for _, k := range keys {
		v := strings.TrimSpace(k.Key)
		if v == "" {
			continue
		}
		out = append(out, model.ChannelKey{Key: v, Enabled: k.Enabled, Label: strings.TrimSpace(k.Label)})
	}
	return out
}

func sanitizeStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if v := strings.TrimSpace(s); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func normalizeModelMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		pub := strings.TrimSpace(k)
		if pub == "" {
			continue
		}
		um := strings.TrimSpace(v)
		if um == "" {
			um = pub
		}
		out[pub] = um
	}
	return out
}

// describeInvalidChatBody 与真实转发同一判定标准，返回失败原因（正常返回空串）。
func describeInvalidChatBody(raw []byte) string {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		return ""
	}
	if e, ok := data["error"].(map[string]any); ok {
		for _, k := range []string{"message", "code", "type"} {
			if s, ok := e[k].(string); ok && s != "" {
				return s
			}
		}
		return "upstream error"
	}
	choices, ok := data["choices"]
	if !ok {
		return "choices 字段缺失"
	}
	arr, ok := choices.([]any)
	if !ok {
		return "choices 字段异常"
	}
	if len(arr) == 0 {
		return "choices 为空"
	}
	return ""
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// clampDays 解析天数参数，缺省值 def，上限 365。
func clampDays(s string, def int) int {
	n := atoiOr(s, def)
	if n <= 0 {
		n = def
	}
	if n > 365 {
		n = 365
	}
	return n
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

func atoi64Or(s string, def int64) int64 {
	if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
		return v
	}
	return def
}

func beijingLocation() *time.Location {
	return time.FixedZone("CST", 8*3600)
}

// beijingDayRange 返回北京时区某日的 [start, end) 毫秒区间。
func beijingDayRange(date string) (int64, int64, error) {
	t, err := time.ParseInLocation("2006-01-02", date, beijingLocation())
	if err != nil {
		return 0, 0, err
	}
	start := t.UnixMilli()
	return start, start + 24*60*60*1000, nil
}