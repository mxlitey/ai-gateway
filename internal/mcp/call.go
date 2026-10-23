package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// errQuotaExhausted 配额用尽的固定文案，handler 据此映射错误码。
const errQuotaExhausted = "今日调用次数已达上限"

// callTool 按密钥策略选号并调用上游工具，成功且计费时记录配额与用量。
//
// 一次调用链只写一条用量日志：attempts 记录实际试过的密钥数。
// 是否计费由服务器的计费口径决定（见 isBillable），默认成功即计费。
func (svc *Service) callTool(ctx context.Context, srv *model.MCPServer, r *http.Request, params map[string]any) (json.RawMessage, error) {
	toolName, _ := params["name"].(string)
	start := time.Now()

	// 工具维度优先：配了日上限（-1 或未配置表示不限）就先按工具配额拦截，超限直接拒绝
	toolLimit, hasToolLimit := srv.ToolLimits[toolName]
	toolLimited := hasToolLimit && toolLimit >= 0
	if toolLimited && svc.quota.ToolUsed(srv.ID, toolName) >= int64(toolLimit) {
		svc.logCall(srv, toolName, "", false, false, 0, 0, time.Since(start), errQuotaExhausted)
		return nil, &upstreamError{Message: errQuotaExhausted}
	}

	keys := svc.orderedKeys(srv)
	if len(keys) == 0 {
		// 无密钥的公开服务：用空密钥发一次（不写 Authorization）
		keys = []string{""}
	}

	ctx, cancel := context.WithTimeout(ctx, toolTimeout(srv))
	defer cancel()

	attempts := 0
	lastErr := ""
	for _, key := range keys {
		// 只有该工具限量时才顺带校验密钥剩余次数；工具不限量时密钥上限不参与拦截
		if toolLimited {
			if ok, reason := svc.quotaLeft(srv, key); !ok {
				lastErr = reason
				continue
			}
		}
		attempts++

		raw, err := svc.client.rpcCall(ctx, srv, key, r, "tools/call", params)
		if err != nil {
			lastErr = err.Error()
			if ue, ok := err.(*upstreamError); ok {
				// 会话失效（404）时清掉缓存，下一次重新协商
				if ue.Status == http.StatusNotFound {
					svc.client.dropSession(srv.ID, key)
				}
				if !ue.Retryable {
					svc.logCall(srv, toolName, key, false, false, ue.Status, attempts, time.Since(start), ue.Message)
					return nil, ue
				}
			}
			continue
		}

		// 计费口径与上游对齐：marker 模式下未命中标记的调用上游不计费，
		// 网关也不占配额、不计入「次数」，否则两边数字会长期对不上。
		billable := isBillable(srv, raw)
		if billable {
			svc.quota.Incr(srv.ID, model.KeyHint(key))
			svc.quota.IncrTool(srv.ID, toolName)
		}
		svc.logCall(srv, toolName, key, true, billable, http.StatusOK, attempts, time.Since(start), "")
		return raw, nil
	}

	svc.logCall(srv, toolName, "", false, false, 0, attempts, time.Since(start), lastErr)
	if lastErr == "" {
		lastErr = "上游不可用"
	}
	return nil, &upstreamError{Message: lastErr}
}

// isBillable 判断一次成功调用是否计入计费次数。
//
// success 模式：成功即计费。marker 模式：响应正文包含标记串才计费，
// 用于上游「模型自行判定无需检索、不产生计费」的场景；标记留空时退化为成功即计费。
func isBillable(srv *model.MCPServer, raw json.RawMessage) bool {
	if model.NormalizeBillableMode(srv.BillableMode) != model.MCPBillableMarker {
		return true
	}
	marker := strings.TrimSpace(srv.BillableMarker)
	if marker == "" {
		return true
	}
	return bytes.Contains(raw, []byte(marker))
}

// quotaLeft 判断某密钥是否还有今日配额。
//
// 先看密钥自身的每日上限，再看服务器的整台上限：
// -1 表示不限（跳过检查），0 表示不允许调用，正数为每日上限。
func (svc *Service) quotaLeft(srv *model.MCPServer, key string) (bool, string) {
	hint := model.KeyHint(key)
	for _, k := range srv.Keys {
		if k.Key != key || k.DailyLimit < 0 {
			continue
		}
		if svc.quota.Used(srv.ID, hint) >= int64(k.DailyLimit) {
			return false, errQuotaExhausted
		}
	}
	if srv.DailyLimit >= 0 && svc.quota.DailyTotal(srv.ID) >= int64(srv.DailyLimit) {
		return false, errQuotaExhausted
	}
	return true, ""
}

// orderedKeys 按密钥策略展开可用密钥：轮询轮流打头，故障转移保持配置顺序。
func (svc *Service) orderedKeys(srv *model.MCPServer) []string {
	keys := srv.EnabledKeys()
	n := len(keys)
	if n == 0 {
		return nil
	}
	if srv.KeyStrategy == model.KeyStrategyOrdered {
		return keys
	}
	start := int(svc.nextRotation(srv.ID) % uint64(n))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, keys[(int(start)+i)%n])
	}
	return out
}

// nextRotation 返回某服务器的下一个轮询序号。
func (svc *Service) nextRotation(serverID string) uint64 {
	svc.rotMu.Lock()
	defer svc.rotMu.Unlock()
	v := svc.rot[serverID]
	svc.rot[serverID] = v + 1
	return v
}

// logCall 异步写入一条调用日志，失败不影响主流程。
func (svc *Service) logCall(srv *model.MCPServer, tool, key string, success, billable bool, status int, attempts int, d time.Duration, errMsg string) {
	if svc.store == nil {
		return
	}
	row := &model.MCPCallLog{
		CreatedAt:  time.Now().UnixMilli(),
		ServerID:   srv.ID,
		ServerName: srv.Name,
		ServerURL:  srv.URL,
		Tool:       tool,
		Success:    success,
		Billable:   billable,
		HTTPStatus: status,
		Attempts:   attempts,
		DurationMS: d.Milliseconds(),
		Error:      strings.TrimSpace(errMsg),
	}
	if key != "" {
		row.KeyHint = model.KeyHint(key)
	}
	go func() {
		if err := svc.store.InsertMCPCallLog(row); err != nil {
			// 日志失败只丢统计，不影响转发
			_ = err
		}
	}()
}

// DailyCalls 返回某服务器当天的计费调用总数（面板展示用）。
func (svc *Service) DailyCalls(serverID string) int64 {
	return svc.quota.DailyTotal(serverID)
}

// DailyCallsByKey 返回某服务器当天各密钥的计费调用次数（key_hint -> 次数）。
func (svc *Service) DailyCallsByKey(serverID string) map[string]int64 {
	return svc.quota.Snapshot(serverID)
}

// DailyCallsByTool 返回某服务器当天各工具的计费调用次数（工具名 -> 次数）。
func (svc *Service) DailyCallsByTool(serverID string) map[string]int64 {
	return svc.quota.ToolSnapshot(serverID)
}

// passthrough 按密钥策略把任意方法原样转发给上游（单服务器模式下的 resources / prompts 等）。
func (svc *Service) passthrough(ctx context.Context, srv *model.MCPServer, r *http.Request, method string, params any) (json.RawMessage, error) {
	keys := svc.orderedKeys(srv)
	if len(keys) == 0 {
		keys = []string{""}
	}
	ctx, cancel := context.WithTimeout(ctx, toolTimeout(srv))
	defer cancel()

	var lastErr error
	for _, key := range keys {
		raw, err := svc.client.rpcCall(ctx, srv, key, r, method, params)
		if err != nil {
			lastErr = err
			if ue, ok := err.(*upstreamError); ok && !ue.Retryable {
				return nil, ue
			}
			continue
		}
		return raw, nil
	}
	if lastErr == nil {
		lastErr = &upstreamError{Message: "上游不可用"}
	}
	return nil, lastErr
}

// CallExposed 按对外工具名执行一次调用（供管理面板试调用复用同一条链路）。
func (svc *Service) CallExposed(ctx context.Context, exposed string, args map[string]any) (json.RawMessage, error) {
	route, ok := svc.registry.lookup(exposed)
	if !ok {
		return nil, &upstreamError{Message: "未知工具: " + exposed}
	}
	srv, err := svc.store.GetMCPServer(route.ServerID)
	if err != nil {
		return nil, err
	}
	if srv == nil || !srv.Enabled {
		return nil, &upstreamError{Message: "该工具所属的服务器已不可用"}
	}
	return svc.callTool(ctx, srv, nil, map[string]any{
		"name":      route.Tool,
		"arguments": args,
	})
}

// Probe 探测某服务器是否可用：调用一次 tools/list（不写入缓存）。
func (svc *Service) Probe(ctx context.Context, srv *model.MCPServer) error {
	ctx, cancel := context.WithTimeout(ctx, toolTimeout(srv))
	defer cancel()
	tools, err := svc.listTools(ctx, srv, nil)
	if err != nil {
		svc.setState(srv.ID, "error", err.Error())
		return err
	}
	svc.setState(srv.ID, "ok", "")
	_ = tools
	return nil
}

// isRetryable 判断错误是否值得换下一个密钥。
func isRetryable(err error) bool {
	var ue *upstreamError
	return errors.As(err, &ue) && ue.Retryable
}

// NormalizeAlias 把任意名称归一化成合法的工具命名空间前缀（供管理接口校验）。
func NormalizeAlias(s string) string { return normalizeAlias(s) }
