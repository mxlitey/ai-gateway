package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/czg95/ai-gateway-v2/internal/config"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// maxToolNameLen 对外工具名长度上限。多数模型 API 对函数名有 64 字符限制，
// 客户端（如 Claude Code）会直接沿用这里的名字注册函数，因此按最严格的口径截断。
const maxToolNameLen = 64

// aliasRe 工具命名空间前缀的合法字符集（校验用）。
const aliasMaxLen = 32

// normalizer 把任意名称归一化成合法的工具名前缀。
func normalizeAlias(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) > aliasMaxLen {
		out = out[:aliasMaxLen]
	}
	return out
}

// exposedName 生成对外工具名：alias__tool，超长时截断并附短哈希保证唯一。
func exposedName(alias, tool string) string {
	name := alias + "__" + tool
	if len(name) <= maxToolNameLen {
		return name
	}
	// 保留可读前缀，用原文的哈希尾巴避免截断后撞名
	suffix := fmt.Sprintf("_%08x", fnv32(alias+"__"+tool))
	keep := maxToolNameLen - len(suffix)
	if keep < 1 {
		keep = 1
	}
	cut := name[:keep]
	// 不要截在多字节字符中间
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size != 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut + suffix
}

// fnv32 计算 FNV-1a 32 位哈希，用于生成稳定的短后缀。
func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// registry 工具路由表：对外名 -> (服务器, 上游工具名)。
type registry struct {
	mu     sync.RWMutex
	byName map[string]toolRoute
}

// toolRoute 一条工具路由。
type toolRoute struct {
	ServerID string
	Tool     string
}

func newRegistry() *registry {
	return &registry{byName: map[string]toolRoute{}}
}

func (r *registry) replace(routes map[string]toolRoute) {
	r.mu.Lock()
	r.byName = routes
	r.mu.Unlock()
}

func (r *registry) lookup(name string) (toolRoute, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.byName[name]
	return rt, ok
}

// toolCache 缓存每台服务器的工具列表，带过期与手动失效。
type toolCache struct {
	mu      sync.Mutex
	entries map[string]toolCacheEntry
}

type toolCacheEntry struct {
	tools   []toolDef
	expires time.Time
}

// toolCacheTTL 工具列表缓存时长；上游 list_changed 或面板刷新会提前失效。
const toolCacheTTL = 60 * time.Second

func newToolCache() *toolCache {
	return &toolCache{entries: map[string]toolCacheEntry{}}
}

func (c *toolCache) get(serverID string) ([]toolDef, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[serverID]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.tools, true
}

func (c *toolCache) put(serverID string, tools []toolDef) {
	c.mu.Lock()
	c.entries[serverID] = toolCacheEntry{tools: tools, expires: time.Now().Add(toolCacheTTL)}
	c.mu.Unlock()
}

func (c *toolCache) invalidate(serverID string) {
	c.mu.Lock()
	delete(c.entries, serverID)
	c.mu.Unlock()
}

// quota 每日调用次数配额的本地计数，避免每次调用都查库。
//
// 计数以「服务器 + 密钥」为单位，跨天自动重置；启动时由 Load 预热，
// 记录成功后自增。库里的日志是权威来源，这里只是高频路径上的缓存。
type quota struct {
	mu     sync.Mutex
	day    string
	counts map[string]int64
}

func newQuota() *quota {
	return &quota{counts: map[string]int64{}}
}

func (q *quota) today() string {
	return time.Now().Format("2006-01-02")
}

// resetIfNeeded 跨天时清空计数。
func (q *quota) resetIfNeeded() {
	day := q.today()
	if q.day != day {
		q.day = day
		q.counts = map[string]int64{}
	}
}

// Load 用库里的统计覆盖内存计数（启动与定期校准）。
func (q *quota) Load(serverID string, sinceMs int64, counts map[string]int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resetIfNeeded()
	for hint, n := range counts {
		q.counts[serverID+"\x00"+hint] = n
	}
}

// LoadTool 用库里的统计覆盖某服务器各上游密钥、各工具的内存计数。
//
// counts 的结构为 工具名 -> key_hint -> 次数。
func (q *quota) LoadTool(serverID string, counts map[string]map[string]int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resetIfNeeded()
	for tool, byHint := range counts {
		for hint, n := range byHint {
			q.counts[toolKey(serverID, tool, hint)] = n
		}
	}
}

// toolKey 工具计数的内部键，用 \x01 前缀与密钥计数区分；
// 工具计数按（服务器, 上游密钥, 工具）独立统计，\x02 分隔密钥与工具名。
func toolKey(serverID, tool, keyHint string) string {
	return serverID + "\x01" + keyHint + "\x02" + tool
}

// ToolUsed 返回某上游密钥当天对某工具的计费调用次数。
func (q *quota) ToolUsed(serverID, tool, keyHint string) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resetIfNeeded()
	return q.counts[toolKey(serverID, tool, keyHint)]
}

// IncrTool 记录某上游密钥对某工具的一次计费调用。
func (q *quota) IncrTool(serverID, tool, keyHint string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resetIfNeeded()
	q.counts[toolKey(serverID, tool, keyHint)]++
}

// Used 返回某密钥当天的计费调用次数。
func (q *quota) Used(serverID, keyHint string) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resetIfNeeded()
	return q.counts[serverID+"\x00"+keyHint]
}

// Incr 记录一次计费调用。
func (q *quota) Incr(serverID, keyHint string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resetIfNeeded()
	q.counts[serverID+"\x00"+keyHint]++
}

// DailyTotal 返回某服务器当天的计费调用总数。
func (q *quota) DailyTotal(serverID string) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resetIfNeeded()
	var n int64
	prefix := serverID + "\x00"
	for k, v := range q.counts {
		if strings.HasPrefix(k, prefix) {
			n += v
		}
	}
	return n
}

// Snapshot 返回某服务器当天各密钥的计费调用次数（key_hint -> 次数）。
func (q *quota) Snapshot(serverID string) map[string]int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resetIfNeeded()
	prefix := serverID + "\x00"
	out := map[string]int64{}
	for k, v := range q.counts {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return out
}

// ToolSnapshot 返回某服务器当天各工具的计费调用次数（工具名 -> 次数）。
//
// 计数按上游密钥独立存放，这里跨密钥汇总用于面板展示。
func (q *quota) ToolSnapshot(serverID string) map[string]int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resetIfNeeded()
	prefix := serverID + "\x01"
	out := map[string]int64{}
	for k, v := range q.counts {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		_, tool, ok := strings.Cut(strings.TrimPrefix(k, prefix), "\x02")
		if !ok {
			continue
		}
		out[tool] += v
	}
	return out
}

// startOfToday 返回本地时区当天 00:00 的毫秒时间戳。
func startOfToday() int64 {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
}

// serverState 每台服务器的运行时状态（探活结果与最近错误），供面板展示。
type serverState struct {
	mu      sync.Mutex
	status  string
	lastErr string
	checked time.Time
}

// defaultToolTimeout 未配置超时时的工具调用上限。
const defaultToolTimeout = 120 * time.Second

// toolTimeout 返回该服务器的调用超时。
func toolTimeout(s *model.MCPServer) time.Duration {
	ms := s.TimeoutMS
	if ms <= 0 {
		return defaultToolTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

// Service MCP 聚合服务：对客户端是一个 MCP 服务器，对上游是若干 MCP 客户端。
type Service struct {
	store  *store.Store
	client *client
	cfg    *config.Config
	local  *localManager

	registry *registry
	cache    *toolCache
	quota    *quota

	mu     sync.RWMutex
	states map[string]*serverState

	// rot 轮询计数：服务器 id -> 下一个首选密钥序号
	rotMu sync.Mutex
	rot   map[string]uint64

	// inflight 防止同一服务器的工具列表被并发重复拉取
	inflight sync.Map
}

// NewService 创建 MCP 聚合服务。
func NewService(s *store.Store, cfg *config.Config) *Service {
	svc := &Service{
		store:    s,
		client:   newClient(),
		cfg:      cfg,
		registry: newRegistry(),
		cache:    newToolCache(),
		quota:    newQuota(),
		states:   map[string]*serverState{},
		rot:      map[string]uint64{},
	}
	svc.local = newLocalManager(cfg, s, svc)
	return svc
}

// Warmup 预热：从库里恢复工具路由与今日配额计数。
func (svc *Service) Warmup() {
	tools, err := svc.store.ListMCPTools()
	if err != nil {
		return
	}
	routes := map[string]toolRoute{}
	for _, t := range tools {
		routes[t.ExposedName] = toolRoute{ServerID: t.ServerID, Tool: t.Name}
	}
	svc.registry.replace(routes)

	servers, err := svc.store.ListMCPServers()
	if err != nil {
		return
	}
	since := startOfToday()
	for i := range servers {
		counts, err := svc.store.MCPDailyCallCounts(servers[i].ID, since)
		if err != nil {
			continue
		}
		svc.quota.Load(servers[i].ID, since, counts)
		if toolCounts, err := svc.store.MCPDailyToolCallCounts(servers[i].ID, since); err == nil {
			svc.quota.LoadTool(servers[i].ID, toolCounts)
		}
	}
}

// stateOf 返回（必要时创建）某服务器的运行时状态。
func (svc *Service) stateOf(serverID string) *serverState {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	st, ok := svc.states[serverID]
	if !ok {
		st = &serverState{status: "idle"}
		svc.states[serverID] = st
	}
	return st
}

func (svc *Service) setState(serverID, status, msg string) {
	st := svc.stateOf(serverID)
	st.mu.Lock()
	st.status = status
	st.lastErr = msg
	st.checked = time.Now()
	st.mu.Unlock()
}

// State 返回某服务器的运行时状态。
func (svc *Service) State(serverID string) (status, lastErr string) {
	st := svc.stateOf(serverID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.status, st.lastErr
}

// RefreshTools 主动拉取某服务器的工具列表并刷新路由表。
func (svc *Service) RefreshTools(ctx context.Context, srv *model.MCPServer) error {
	svc.cache.invalidate(srv.ID)
	tools, err := svc.listTools(ctx, srv, nil)
	if err != nil {
		svc.setState(srv.ID, "error", err.Error())
		return err
	}
	svc.setState(srv.ID, "ok", "")
	return svc.saveTools(srv, tools)
}

// saveTools 把工具列表写入缓存与数据库，并重建对外路由。
// tools/list 与手动刷新都走这里，保证 tools/call 一定能查到刚列出的工具。
func (svc *Service) saveTools(srv *model.MCPServer, tools []toolDef) error {
	svc.cache.put(srv.ID, tools)

	rows := make([]model.MCPTool, 0, len(tools))
	alias := serverAlias(srv)
	for _, t := range tools {
		rows = append(rows, model.MCPTool{
			ServerID:    srv.ID,
			Name:        t.Name,
			ExposedName: exposedName(alias, t.Name),
			Raw:         string(t.Raw),
		})
	}
	if err := svc.store.ReplaceMCPTools(srv.ID, rows); err != nil {
		return err
	}
	return svc.rebuildRegistry()
}

// rebuildRegistry 依据库里的工具缓存重建路由表。
func (svc *Service) rebuildRegistry() error {
	tools, err := svc.store.ListMCPTools()
	if err != nil {
		return err
	}
	routes := make(map[string]toolRoute, len(tools))
	for _, t := range tools {
		routes[t.ExposedName] = toolRoute{ServerID: t.ServerID, Tool: t.Name}
	}
	svc.registry.replace(routes)
	return nil
}

// RebuildRoutes 重新读取工具缓存并重建路由表（删除服务器后调用）。
func (svc *Service) RebuildRoutes() {
	_ = svc.rebuildRegistry()
}

// ForgetTools 清除某服务器的工具缓存与运行时状态（删除服务器后调用）。
func (svc *Service) ForgetTools(serverID string) {
	svc.cache.invalidate(serverID)
	svc.mu.Lock()
	delete(svc.states, serverID)
	svc.mu.Unlock()
}

// serverAlias 返回服务器的工具命名空间前缀。
func serverAlias(s *model.MCPServer) string {
	if a := strings.TrimSpace(s.Alias); a != "" {
		return a
	}
	if a := normalizeAlias(s.Name); a != "" {
		return a
	}
	return normalizeAlias(s.ID)
}

// listTools 拉取某服务器的工具列表：按密钥策略依次尝试，第一个成功即返回。
func (svc *Service) listTools(ctx context.Context, srv *model.MCPServer, r *http.Request) ([]toolDef, error) {
	keys := srv.EnabledKeys()
	if len(keys) == 0 {
		keys = []string{""} // 无密钥的公开服务
	}

	var lastErr error
	for _, key := range keys {
		raw, err := svc.client.rpcCall(ctx, srv, key, r, "tools/list", map[string]any{})
		if err != nil {
			lastErr = err
			if ue, ok := err.(*upstreamError); ok && !ue.Retryable {
				return nil, err
			}
			continue
		}
		var doc struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("解析工具列表失败: %w", err)
		}
		out := make([]toolDef, 0, len(doc.Tools))
		for _, item := range doc.Tools {
			var head struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(item, &head); err != nil || strings.TrimSpace(head.Name) == "" {
				continue
			}
			out = append(out, toolDef{Name: head.Name, Raw: item})
		}
		return out, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用密钥")
	}
	return nil, lastErr
}

// cachedTools 读取工具列表，未命中缓存时拉取（加并发去重）。
func (svc *Service) cachedTools(ctx context.Context, srv *model.MCPServer, r *http.Request) ([]toolDef, error) {
	if tools, ok := svc.cache.get(srv.ID); ok {
		return tools, nil
	}
	// 同一服务器的并发请求只让一个去拉取，其余复用结果
	lock, loaded := svc.inflight.LoadOrStore(srv.ID, make(chan struct{}))
	if loaded {
		select {
		case <-lock.(chan struct{}):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if tools, ok := svc.cache.get(srv.ID); ok {
			return tools, nil
		}
		return nil, fmt.Errorf("工具列表拉取失败")
	}
	ch := lock.(chan struct{})
	defer func() {
		svc.inflight.Delete(srv.ID)
		close(ch)
	}()

	tools, err := svc.listTools(ctx, srv, r)
	if err != nil {
		svc.setState(srv.ID, "error", err.Error())
		return nil, err
	}
	svc.setState(srv.ID, "ok", "")
	// 首次列出后立即落库并注册路由，否则 tools/call 会查不到这些工具
	_ = svc.saveTools(srv, tools)
	return tools, nil
}
