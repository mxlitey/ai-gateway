package store

import (
	"database/sql"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// ---------- MCP 上游服务器 ----------

const mcpServerCols = `id, name, alias, url, headers, keys, key_strategy, daily_limit, tool_limits, timeout_ms, enabled, source, sort_order, created_at`

func scanMCPServer(sc scanner) (*model.MCPServer, error) {
	s := &model.MCPServer{
		Headers:    []model.Header{},
		Keys:       []model.MCPKey{},
		ToolLimits: map[string]int{},
	}
	var headersS, keysS, toolLimitsS string
	var enabled int
	if err := sc.Scan(
		&s.ID, &s.Name, &s.Alias, &s.URL, &headersS, &keysS,
		&s.KeyStrategy, &s.DailyLimit, &toolLimitsS, &s.TimeoutMS, &enabled, &s.Source, &s.SortOrder, &s.CreatedAt,
	); err != nil {
		return nil, err
	}
	s.Enabled = enabled != 0
	s.KeyStrategy = model.NormalizeKeyStrategy(s.KeyStrategy)
	if err := decodeJSON(headersS, &s.Headers); err != nil {
		return nil, err
	}
	if err := decodeJSON(keysS, &s.Keys); err != nil {
		return nil, err
	}
	if err := decodeJSON(toolLimitsS, &s.ToolLimits); err != nil {
		return nil, err
	}
	if s.Headers == nil {
		s.Headers = []model.Header{}
	}
	if s.Keys == nil {
		s.Keys = []model.MCPKey{}
	}
	if s.ToolLimits == nil {
		s.ToolLimits = map[string]int{}
	}
	return s, nil
}

// ListMCPServers 按展示顺序返回全部 MCP 服务器。
func (s *Store) ListMCPServers() ([]model.MCPServer, error) {
	rows, err := s.db.Query(`SELECT ` + mcpServerCols + ` FROM mcp_servers ORDER BY sort_order ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.MCPServer{}
	for rows.Next() {
		srv, err := scanMCPServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *srv)
	}
	return out, rows.Err()
}

// GetMCPServer 按 id 返回 MCP 服务器，不存在时返回 (nil, nil)。
func (s *Store) GetMCPServer(id string) (*model.MCPServer, error) {
	row := s.db.QueryRow(`SELECT `+mcpServerCols+` FROM mcp_servers WHERE id = ?`, id)
	srv, err := scanMCPServer(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return srv, nil
}

// NextMCPServerSortOrder 返回下一个展示序号。
func (s *Store) NextMCPServerSortOrder() (int64, error) {
	var max sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(sort_order) FROM mcp_servers`).Scan(&max); err != nil {
		return 0, err
	}
	if !max.Valid {
		return 0, nil
	}
	return max.Int64 + 1, nil
}

// CreateMCPServer 新建 MCP 服务器。
func (s *Store) CreateMCPServer(srv *model.MCPServer) error {
	headers, err := marshalJSON(srv.Headers)
	if err != nil {
		return err
	}
	keys, err := marshalJSON(srv.Keys)
	if err != nil {
		return err
	}
	toolLimits, err := marshalJSON(srv.ToolLimits)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO mcp_servers
		 (id, name, alias, url, headers, keys, key_strategy, daily_limit, tool_limits, timeout_ms, enabled, source, sort_order, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		srv.ID, srv.Name, srv.Alias, srv.URL, headers, keys,
		model.NormalizeKeyStrategy(srv.KeyStrategy), srv.DailyLimit, toolLimits, srv.TimeoutMS,
		boolToInt(srv.Enabled), srv.Source, srv.SortOrder, srv.CreatedAt,
	)
	return err
}

// UpdateMCPServer 更新 MCP 服务器的可变字段。
func (s *Store) UpdateMCPServer(srv *model.MCPServer) error {
	headers, err := marshalJSON(srv.Headers)
	if err != nil {
		return err
	}
	keys, err := marshalJSON(srv.Keys)
	if err != nil {
		return err
	}
	toolLimits, err := marshalJSON(srv.ToolLimits)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE mcp_servers SET
		   name = ?, alias = ?, url = ?, headers = ?, keys = ?, key_strategy = ?,
		   daily_limit = ?, tool_limits = ?, timeout_ms = ?, enabled = ?
		 WHERE id = ?`,
		srv.Name, srv.Alias, srv.URL, headers, keys, model.NormalizeKeyStrategy(srv.KeyStrategy),
		srv.DailyLimit, toolLimits, srv.TimeoutMS, boolToInt(srv.Enabled), srv.ID,
	)
	return err
}

// DeleteMCPServer 删除 MCP 服务器，同时清理其工具缓存。
func (s *Store) DeleteMCPServer(id string) error {
	if _, err := s.db.Exec(`DELETE FROM mcp_tools WHERE server_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM mcp_servers WHERE id = ?`, id)
	return err
}

// UsedMCPAliases 返回已被占用的工具命名空间前缀（含自身 id），用于校验别名冲突。
func (s *Store) UsedMCPAliases() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT alias FROM mcp_servers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, err
		}
		if a := strings.ToLower(strings.TrimSpace(alias)); a != "" {
			out[a] = true
		}
	}
	return out, rows.Err()
}

// ---------- 工具缓存 ----------

// ReplaceMCPTools 以一次拉取结果整体替换某服务器的工具缓存。
func (s *Store) ReplaceMCPTools(serverID string, tools []model.MCPTool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM mcp_tools WHERE server_id = ?`, serverID); err != nil {
		return err
	}
	if len(tools) > 0 {
		stmt, err := tx.Prepare(
			`INSERT INTO mcp_tools (server_id, name, exposed_name, raw, updated_at) VALUES (?,?,?,?,?)`,
		)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()

		now := time.Now().UnixMilli()
		for i := range tools {
			t := &tools[i]
			if t.UpdatedAt == 0 {
				t.UpdatedAt = now
			}
			if _, err := stmt.Exec(serverID, t.Name, t.ExposedName, t.Raw, t.UpdatedAt); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// ListMCPTools 返回全部工具缓存（按对外暴露名排序）。
func (s *Store) ListMCPTools() ([]model.MCPTool, error) {
	rows, err := s.db.Query(
		`SELECT server_id, name, exposed_name, raw, updated_at FROM mcp_tools ORDER BY exposed_name ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.MCPTool{}
	for rows.Next() {
		var t model.MCPTool
		if err := rows.Scan(&t.ServerID, &t.Name, &t.ExposedName, &t.Raw, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountMCPToolsByServer 返回每个服务器的工具数量。
func (s *Store) CountMCPToolsByServer() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT server_id, COUNT(1) FROM mcp_tools GROUP BY server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ---------- 调用日志与配额 ----------

// InsertMCPCallLog 写入一条工具调用记录。
func (s *Store) InsertMCPCallLog(l *model.MCPCallLog) error {
	if l.CreatedAt == 0 {
		l.CreatedAt = time.Now().UnixMilli()
	}
	res, err := s.db.Exec(
		`INSERT INTO mcp_call_logs
		 (created_at, server_id, server_name, server_url, tool, key_hint, success, http_status, attempts, duration_ms, error)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		l.CreatedAt, l.ServerID, l.ServerName, l.ServerURL, l.Tool, l.KeyHint,
		boolToInt(l.Success), l.HTTPStatus, l.Attempts, l.DurationMS, truncateForDB(l.Error, 2000),
	)
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil {
		l.ID = id
	}
	return nil
}

// MCPDailyCallCounts 统计某服务器自 sinceMs 起各密钥的成功调用次数（key_hint -> 次数）。
// 返回 map 的 "" 键为没有记录密钥提示的历史调用，调用方只看具体密钥即可。
func (s *Store) MCPDailyCallCounts(serverID string, sinceMs int64) (map[string]int64, error) {
	rows, err := s.db.Query(
		`SELECT key_hint, COUNT(1) FROM mcp_call_logs
		 WHERE server_id = ? AND success = 1 AND created_at >= ?
		 GROUP BY key_hint`,
		serverID, sinceMs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var hint string
		var n int64
		if err := rows.Scan(&hint, &n); err != nil {
			return nil, err
		}
		out[hint] = n
	}
	return out, rows.Err()
}

// MCPDailyToolCallCounts 统计某服务器自 sinceMs 起各工具的成功调用次数（工具名 -> 次数）。
func (s *Store) MCPDailyToolCallCounts(serverID string, sinceMs int64) (map[string]int64, error) {
	rows, err := s.db.Query(
		`SELECT tool, COUNT(1) FROM mcp_call_logs
		 WHERE server_id = ? AND success = 1 AND created_at >= ?
		 GROUP BY tool`,
		serverID, sinceMs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var tool string
		var n int64
		if err := rows.Scan(&tool, &n); err != nil {
			return nil, err
		}
		out[tool] = n
	}
	return out, rows.Err()
}

// MCPCallLogFilter 调用日志的查询条件（零值表示不过滤）。
type MCPCallLogFilter struct {
	FromMs   int64
	ToMs     int64
	ServerID string
	Tool     string
	OnlyFail bool
	Limit    int
	Offset   int
}

func (f MCPCallLogFilter) where() (string, []any) {
	conds := []string{"created_at >= ?", "created_at < ?"}
	args := []any{f.FromMs, f.ToMs}
	if f.ServerID != "" {
		conds = append(conds, "server_id = ?")
		args = append(args, f.ServerID)
	}
	if f.Tool != "" {
		conds = append(conds, "tool = ?")
		args = append(args, f.Tool)
	}
	if f.OnlyFail {
		conds = append(conds, "success = 0")
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListMCPCallLogs 按时间倒序返回调用明细。
func (s *Store) ListMCPCallLogs(f MCPCallLogFilter) ([]model.MCPCallLog, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where, args := f.where()
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.Query(
		`SELECT id, created_at, server_id, server_name, server_url, tool, key_hint,
		        success, http_status, attempts, duration_ms, error
		 FROM mcp_call_logs`+where+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.MCPCallLog{}
	for rows.Next() {
		var l model.MCPCallLog
		var success int
		if err := rows.Scan(
			&l.ID, &l.CreatedAt, &l.ServerID, &l.ServerName, &l.ServerURL, &l.Tool, &l.KeyHint,
			&success, &l.HTTPStatus, &l.Attempts, &l.DurationMS, &l.Error,
		); err != nil {
			return nil, err
		}
		l.Success = success != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

// CountMCPCallLogs 返回符合条件的调用条数（分页用）。
func (s *Store) CountMCPCallLogs(f MCPCallLogFilter) (int64, error) {
	where, args := f.where()
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(1) FROM mcp_call_logs`+where, args...).Scan(&n)
	return n, err
}

// MCPUsageSummary 汇总时间区间内的 MCP 调用次数。
func (s *Store) MCPUsageSummary(fromMs, toMs int64) (*model.MCPUsageSummary, error) {
	sum := &model.MCPUsageSummary{From: fromMs, To: toMs}
	where := "created_at >= ? AND created_at < ?"
	args := []any{fromMs, toMs}

	if err := s.db.QueryRow(
		`SELECT COUNT(1), COALESCE(SUM(success), 0) FROM mcp_call_logs WHERE `+where,
		args...,
	).Scan(&sum.Calls, &sum.Success); err != nil {
		return nil, err
	}
	sum.Failures = sum.Calls - sum.Success

	group := func(expr string) ([]model.MCPUsageBucket, error) {
		rows, err := s.db.Query(
			`SELECT `+expr+` AS k, COUNT(1), COALESCE(SUM(success), 0)
			 FROM mcp_call_logs WHERE `+where+` GROUP BY k ORDER BY 2 DESC`,
			args...,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		out := []model.MCPUsageBucket{}
		for rows.Next() {
			b := model.MCPUsageBucket{}
			if err := rows.Scan(&b.Key, &b.Calls, &b.Success); err != nil {
				return nil, err
			}
			b.Failures = b.Calls - b.Success
			out = append(out, b)
		}
		return out, rows.Err()
	}

	var err error
	if sum.ByServer, err = group("server_name"); err != nil {
		return nil, err
	}
	if sum.ByTool, err = group("tool"); err != nil {
		return nil, err
	}
	if sum.ByDay, err = group(`strftime('%Y-%m-%d', created_at / 1000, 'unixepoch', '+8 hours')`); err != nil {
		return nil, err
	}
	for i, j := 0, len(sum.ByDay)-1; i < j; i, j = i+1, j-1 {
		sum.ByDay[i], sum.ByDay[j] = sum.ByDay[j], sum.ByDay[i]
	}

	if sum.ByKey, err = s.mcpKeyUsage(where, args); err != nil {
		return nil, err
	}
	return sum, nil
}

// mcpKeyUsage 按「服务器 + 密钥」维度汇总调用次数，并补上当前的配置信息。
//
// 失败的调用不记录密钥（一次调用链可能试过多把密钥，无法归因到某一把），
// 所以这里只统计成功调用；配置里存在但本期没被用到的密钥也会列出（次数 0），
// 便于看出「哪把密钥一直没轮到」。已从配置里删除的密钥只保留其历史统计。
func (s *Store) mcpKeyUsage(where string, args []any) ([]model.MCPKeyUsage, error) {
	rows, err := s.db.Query(
		`SELECT server_id, server_name, key_hint, COUNT(1)
		 FROM mcp_call_logs
		 WHERE `+where+` AND success = 1 AND key_hint <> ''
		 GROUP BY server_id, key_hint ORDER BY 4 DESC`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type keyStat struct {
		serverName string
		calls      int64
	}
	stats := map[string]keyStat{} // serverID \x00 key_hint
	order := []string{}
	for rows.Next() {
		var serverID, serverName, hint string
		var calls int64
		if err := rows.Scan(&serverID, &serverName, &hint, &calls); err != nil {
			return nil, err
		}
		k := serverID + "\x00" + hint
		stats[k] = keyStat{serverName: serverName, calls: calls}
		order = append(order, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	servers, err := s.ListMCPServers()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*model.MCPServer, len(servers))
	for i := range servers {
		byID[servers[i].ID] = &servers[i]
	}

	out := make([]model.MCPKeyUsage, 0, len(order))
	emit := func(serverID, serverName, hint, label string, calls int64, limit int, enabled bool) {
		out = append(out, model.MCPKeyUsage{
			ServerID:   serverID,
			ServerName: serverName,
			KeyHint:    hint,
			Label:      label,
			Calls:      calls,
			Success:    calls,
			DailyLimit: limit,
			Enabled:    enabled,
			Unlimited:  limit <= 0,
		})
	}

	// 先按调用量输出有记录的密钥
	emitted := map[string]bool{}
	for _, k := range order {
		st := stats[k]
		serverID, hint, _ := strings.Cut(k, "\x00")
		label, limit, enabled := "", 0, false
		if srv := byID[serverID]; srv != nil {
			for _, key := range srv.Keys {
				if model.KeyHint(key.Key) == hint {
					label, limit, enabled = key.Label, key.DailyLimit, key.Enabled
					break
				}
			}
		}
		emitted[k] = true
		emit(serverID, st.serverName, hint, label, st.calls, limit, enabled)
	}

	// 再补上配置里存在、但本期一次都没被用到的密钥
	for i := range servers {
		srv := &servers[i]
		for _, key := range srv.Keys {
			hint := model.KeyHint(key.Key)
			if emitted[srv.ID+"\x00"+hint] {
				continue
			}
			emit(srv.ID, srv.Name, hint, key.Label, 0, key.DailyLimit, key.Enabled)
		}
	}
	return out, nil
}

// CleanupMCPCallLogsBefore 删除指定时间之前的调用日志，返回删除条数。
func (s *Store) CleanupMCPCallLogsBefore(beforeMs int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM mcp_call_logs WHERE created_at < ?`, beforeMs)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}
