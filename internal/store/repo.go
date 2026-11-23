package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// ---------- JSON 列辅助 ----------

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeJSON(s string, dst any) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), dst)
}

// ---------- 渠道 ----------

const channelCols = `id, name, base_url, path, keys, models, model_map, model_priority, disabled_models, headers, image_models, image_generation, param_map, static_params, drop_params, key_strategy, enabled, source, sort_order, created_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanChannel(sc scanner) (*model.Channel, error) {
	c := &model.Channel{
		Keys:           []model.ChannelKey{},
		Models:         []string{},
		ModelMap:       map[string]string{},
		ModelPriority:  map[string]int{},
		DisabledModels: []string{},
		Headers:        []model.Header{},
		ImageModels:    []string{},
		ParamMap:       map[string]string{},
		StaticParams:   map[string]any{},
		DropParams:     []string{},
	}
	var keysS, modelsS, mapS, priS, disabledS, headersS, imageModelsS, imageGenS string
	var paramMapS, staticParamsS, dropParamsS string
	var enabled int
	if err := sc.Scan(
		&c.ID, &c.Name, &c.BaseURL, &c.Path,
		&keysS, &modelsS, &mapS, &priS, &disabledS, &headersS,
		&imageModelsS, &imageGenS,
		&paramMapS, &staticParamsS, &dropParamsS,
		&c.KeyStrategy, &enabled, &c.Source, &c.SortOrder, &c.CreatedAt,
	); err != nil {
		return nil, err
	}
	c.Enabled = enabled != 0
	c.KeyStrategy = model.NormalizeKeyStrategy(c.KeyStrategy)
	if err := decodeJSON(keysS, &c.Keys); err != nil {
		return nil, err
	}
	if err := decodeJSON(modelsS, &c.Models); err != nil {
		return nil, err
	}
	if err := decodeJSON(mapS, &c.ModelMap); err != nil {
		return nil, err
	}
	if err := decodeJSON(priS, &c.ModelPriority); err != nil {
		return nil, err
	}
	if err := decodeJSON(disabledS, &c.DisabledModels); err != nil {
		return nil, err
	}
	if err := decodeJSON(headersS, &c.Headers); err != nil {
		return nil, err
	}
	if err := decodeJSON(imageModelsS, &c.ImageModels); err != nil {
		return nil, err
	}
	if err := decodeJSON(imageGenS, &c.ImageGen); err != nil {
		return nil, err
	}
	c.ImageGen = c.ImageGen.Normalized()
	if err := decodeJSON(paramMapS, &c.ParamMap); err != nil {
		return nil, err
	}
	if err := decodeJSON(staticParamsS, &c.StaticParams); err != nil {
		return nil, err
	}
	if err := decodeJSON(dropParamsS, &c.DropParams); err != nil {
		return nil, err
	}
	// JSON 中可能为 null，退回空集合避免下游判空
	if c.Keys == nil {
		c.Keys = []model.ChannelKey{}
	}
	if c.Models == nil {
		c.Models = []string{}
	}
	if c.ModelMap == nil {
		c.ModelMap = map[string]string{}
	}
	if c.ModelPriority == nil {
		c.ModelPriority = map[string]int{}
	}
	if c.DisabledModels == nil {
		c.DisabledModels = []string{}
	}
	if c.Headers == nil {
		c.Headers = []model.Header{}
	}
	if c.ImageModels == nil {
		c.ImageModels = []string{}
	}
	if c.ParamMap == nil {
		c.ParamMap = map[string]string{}
	}
	if c.StaticParams == nil {
		c.StaticParams = map[string]any{}
	}
	if c.DropParams == nil {
		c.DropParams = []string{}
	}
	return c, nil
}

// ListChannels 按展示顺序返回全部渠道。
//
// 这是最热的读路径：每个 /v1 请求（选路、模型列表）都要用，而渠道变更是低频操作。
// 这里返回缓存的只读快照，调用方不得原地修改（避免并发读写同一份切片）。
// 写渠道的方法会先 bumpChannelsGen 让缓存失效。
func (s *Store) ListChannels() ([]model.Channel, error) {
	if list, ok := s.cachedChannels(); ok {
		return list, nil
	}

	gen := s.channelsGen()
	rows, err := s.db.Query(`SELECT ` + channelCols + ` FROM channels ORDER BY sort_order ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.storeChannels(gen, out), nil
}

// channelsGen 读取当前渠道版本号。
func (s *Store) channelsGen() uint64 {
	s.channels.mu.RLock()
	defer s.channels.mu.RUnlock()
	return s.channels.gen
}

// bumpChannelsGen 让渠道快照失效。必须在写渠道之前调用：
// 若写入完成后才自增，并发读可能把写入前的旧数据装进新版本号下。
func (s *Store) bumpChannelsGen() {
	s.channels.mu.Lock()
	s.channels.gen++
	s.channels.has = false
	s.channels.list = nil
	s.channels.mu.Unlock()
}

// cachedChannels 命中时返回快照副本视图。
func (s *Store) cachedChannels() ([]model.Channel, bool) {
	s.channels.mu.RLock()
	defer s.channels.mu.RUnlock()
	if !s.channels.has || s.channels.cachedGen != s.channels.gen {
		return nil, false
	}
	return s.channels.list, true
}

// storeChannels 在版本号未变化时装入快照，并返回本次结果。
// 版本号已变化说明期间发生过写入，不装缓存（本次结果可能是旧数据，
// 但调用方仍需要一份数据，后续读会重新查询）。
func (s *Store) storeChannels(gen uint64, list []model.Channel) []model.Channel {
	s.channels.mu.Lock()
	defer s.channels.mu.Unlock()
	if s.channels.gen != gen {
		return list
	}
	s.channels.list = list
	s.channels.cachedGen = gen
	s.channels.has = true
	return list
}

// GetChannel 按 id 返回渠道，不存在时返回 (nil, nil)。
func (s *Store) GetChannel(id string) (*model.Channel, error) {
	row := s.db.QueryRow(`SELECT `+channelCols+` FROM channels WHERE id = ?`, id)
	c, err := scanChannel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// NextChannelSortOrder 返回下一个展示序号。
func (s *Store) NextChannelSortOrder() (int64, error) {
	var max sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(sort_order) FROM channels`).Scan(&max); err != nil {
		return 0, err
	}
	if !max.Valid {
		return 0, nil
	}
	return max.Int64 + 1, nil
}

// CreateChannel 新建渠道。
func (s *Store) CreateChannel(c *model.Channel) error {
	keys, err := marshalJSON(c.Keys)
	if err != nil {
		return err
	}
	models, err := marshalJSON(c.Models)
	if err != nil {
		return err
	}
	mm, err := marshalJSON(c.ModelMap)
	if err != nil {
		return err
	}
	mp, err := marshalJSON(c.ModelPriority)
	if err != nil {
		return err
	}
	dm, err := marshalJSON(c.DisabledModels)
	if err != nil {
		return err
	}
	hd, err := marshalJSON(c.Headers)
	if err != nil {
		return err
	}
	im, err := marshalJSON(c.ImageModels)
	if err != nil {
		return err
	}
	c.ImageGen = c.ImageGen.Normalized()
	ig, err := marshalJSON(c.ImageGen)
	if err != nil {
		return err
	}
	pm, err := marshalJSON(c.ParamMap)
	if err != nil {
		return err
	}
	sp, err := marshalJSON(c.StaticParams)
	if err != nil {
		return err
	}
	dp, err := marshalJSON(c.DropParams)
	if err != nil {
		return err
	}
	c.KeyStrategy = model.NormalizeKeyStrategy(c.KeyStrategy)
	s.bumpChannelsGen()
	_, err = s.db.Exec(
		`INSERT INTO channels
		 (id, name, base_url, path, keys, models, model_map, model_priority, disabled_models, headers, image_models, image_generation, param_map, static_params, drop_params, key_strategy, enabled, source, sort_order, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.BaseURL, c.Path, keys, models, mm, mp, dm, hd, im, ig, pm, sp, dp,
		c.KeyStrategy, boolToInt(c.Enabled), c.Source, c.SortOrder, c.CreatedAt,
	)
	return err
}

// UpdateChannel 更新渠道的可变字段。
func (s *Store) UpdateChannel(c *model.Channel) error {
	keys, err := marshalJSON(c.Keys)
	if err != nil {
		return err
	}
	models, err := marshalJSON(c.Models)
	if err != nil {
		return err
	}
	mm, err := marshalJSON(c.ModelMap)
	if err != nil {
		return err
	}
	mp, err := marshalJSON(c.ModelPriority)
	if err != nil {
		return err
	}
	dm, err := marshalJSON(c.DisabledModels)
	if err != nil {
		return err
	}
	hd, err := marshalJSON(c.Headers)
	if err != nil {
		return err
	}
	im, err := marshalJSON(c.ImageModels)
	if err != nil {
		return err
	}
	c.ImageGen = c.ImageGen.Normalized()
	ig, err := marshalJSON(c.ImageGen)
	if err != nil {
		return err
	}
	pm, err := marshalJSON(c.ParamMap)
	if err != nil {
		return err
	}
	sp, err := marshalJSON(c.StaticParams)
	if err != nil {
		return err
	}
	dp, err := marshalJSON(c.DropParams)
	if err != nil {
		return err
	}
	c.KeyStrategy = model.NormalizeKeyStrategy(c.KeyStrategy)
	s.bumpChannelsGen()
	_, err = s.db.Exec(
		`UPDATE channels SET
		   name = ?, base_url = ?, path = ?, keys = ?, models = ?,
		   model_map = ?, model_priority = ?, disabled_models = ?, headers = ?,
		   image_models = ?, image_generation = ?,
		   param_map = ?, static_params = ?, drop_params = ?,
		   key_strategy = ?, enabled = ?
		 WHERE id = ?`,
		c.Name, c.BaseURL, c.Path, keys, models, mm, mp, dm, hd, im, ig, pm, sp, dp,
		c.KeyStrategy, boolToInt(c.Enabled), c.ID,
	)
	return err
}

// DeleteChannel 删除渠道。
func (s *Store) DeleteChannel(id string) error {
	s.bumpChannelsGen()
	_, err := s.db.Exec(`DELETE FROM channels WHERE id = ?`, id)
	return err
}

// ---------- 客户端密钥 ----------

const apiKeyCols = `id, name, key, channel_scope, channel_ids, mcp_scope, mcp_server_ids, storage_scope, enabled, created_at`

func scanAPIKey(sc scanner) (*model.APIKey, error) {
	k := &model.APIKey{ChannelIDs: []string{}, MCPServerIDs: []string{}}
	var chS, mcpS string
	var enabled int
	if err := sc.Scan(&k.ID, &k.Name, &k.Key, &k.ChannelScope, &chS, &k.MCPScope, &mcpS, &k.StorageScope, &enabled, &k.CreatedAt); err != nil {
		return nil, err
	}
	k.Enabled = enabled != 0
	if err := decodeJSON(chS, &k.ChannelIDs); err != nil {
		return nil, err
	}
	if err := decodeJSON(mcpS, &k.MCPServerIDs); err != nil {
		return nil, err
	}
	if k.ChannelIDs == nil {
		k.ChannelIDs = []string{}
	}
	if k.MCPServerIDs == nil {
		k.MCPServerIDs = []string{}
	}
	// 兜底：范围字段缺失时按旧语义（全部可用）
	if k.ChannelScope == "" {
		k.ChannelScope = model.ScopeAll
	}
	if k.MCPScope == "" {
		k.MCPScope = model.ScopeAll
	}
	if k.StorageScope == "" {
		k.StorageScope = model.ScopeAll
	}
	return k, nil
}

// ListAPIKeys 返回全部客户端密钥。
func (s *Store) ListAPIKeys() ([]model.APIKey, error) {
	rows, err := s.db.Query(`SELECT ` + apiKeyCols + ` FROM api_keys ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// GetAPIKey 按 id 返回密钥。
func (s *Store) GetAPIKey(id string) (*model.APIKey, error) {
	row := s.db.QueryRow(`SELECT `+apiKeyCols+` FROM api_keys WHERE id = ?`, id)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

// FindEnabledAPIKeyByValue 按密钥值查找已启用的客户端密钥。
func (s *Store) FindEnabledAPIKeyByValue(value string) (*model.APIKey, error) {
	row := s.db.QueryRow(`SELECT `+apiKeyCols+` FROM api_keys WHERE key = ? AND enabled = 1`, value)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

// CreateAPIKey 新建密钥。
func (s *Store) CreateAPIKey(k *model.APIKey) error {
	ch, err := marshalJSON(k.ChannelIDs)
	if err != nil {
		return err
	}
	mcpIDs, err := marshalJSON(k.MCPServerIDs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO api_keys (id, name, key, channel_scope, channel_ids, mcp_scope, mcp_server_ids, storage_scope, enabled, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		k.ID, k.Name, k.Key, k.ChannelScope, ch, k.MCPScope, mcpIDs, k.StorageScope, boolToInt(k.Enabled), k.CreatedAt,
	)
	return err
}

// UpdateAPIKey 更新密钥（名称 / 渠道权限 / MCP 权限 / 对象存储权限 / 启停）。
func (s *Store) UpdateAPIKey(k *model.APIKey) error {
	ch, err := marshalJSON(k.ChannelIDs)
	if err != nil {
		return err
	}
	mcpIDs, err := marshalJSON(k.MCPServerIDs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE api_keys SET name = ?, channel_scope = ?, channel_ids = ?, mcp_scope = ?, mcp_server_ids = ?, storage_scope = ?, enabled = ? WHERE id = ?`,
		k.Name, k.ChannelScope, ch, k.MCPScope, mcpIDs, k.StorageScope, boolToInt(k.Enabled), k.ID,
	)
	return err
}

// DeleteAPIKey 删除密钥。
func (s *Store) DeleteAPIKey(id string) error {
	_, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

// ---------- 错误日志 ----------

// InsertErrorLog 写入一条错误日志。
func (s *Store) InsertErrorLog(e *model.ErrorLog) error {
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.Exec(
		`INSERT INTO error_logs
		 (channel_id, channel_name, base_url, model, upstream_model, status, key_hint, message, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		e.ChannelID, e.ChannelName, e.BaseURL, e.Model, e.UpstreamModel,
		e.Status, e.KeyHint, e.Message, e.CreatedAt,
	)
	return err
}

// ListErrorLogs 查询时间区间内的全部错误日志（倒序）。
//
// 不按渠道过滤：渠道被删除后，它产生的历史错误仍需可查。
func (s *Store) ListErrorLogs(fromMs, toMs int64) ([]model.ErrorLog, error) {
	rows, err := s.db.Query(
		`SELECT id, channel_id, channel_name, base_url, model, upstream_model, status, key_hint, message, created_at
		 FROM error_logs WHERE created_at >= ? AND created_at < ?
		 ORDER BY created_at DESC LIMIT 2000`,
		fromMs, toMs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.ErrorLog{}
	for rows.Next() {
		var e model.ErrorLog
		if err := rows.Scan(
			&e.ID, &e.ChannelID, &e.ChannelName, &e.BaseURL, &e.Model,
			&e.UpstreamModel, &e.Status, &e.KeyHint, &e.Message, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		e.Source = errorSource(e.ChannelID)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListErrorLogsByChannel 查询某渠道在时间区间内的错误日志（倒序）。
func (s *Store) ListErrorLogsByChannel(channelID string, fromMs, toMs int64) ([]model.ErrorLog, error) {
	rows, err := s.db.Query(
		`SELECT id, channel_id, channel_name, base_url, model, upstream_model, status, key_hint, message, created_at
		 FROM error_logs WHERE channel_id = ? AND created_at >= ? AND created_at < ?
		 ORDER BY created_at DESC LIMIT 500`,
		channelID, fromMs, toMs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.ErrorLog{}
	for rows.Next() {
		var e model.ErrorLog
		if err := rows.Scan(
			&e.ID, &e.ChannelID, &e.ChannelName, &e.BaseURL, &e.Model,
			&e.UpstreamModel, &e.Status, &e.KeyHint, &e.Message, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		e.Source = errorSource(e.ChannelID)
		out = append(out, e)
	}
	return out, rows.Err()
}

// errorSource 按是否关联渠道推导错误来源：没有渠道即转发前被网关拒绝。
func errorSource(channelID string) string {
	if channelID == "" {
		return model.ErrorSourceGateway
	}
	return model.ErrorSourceUpstream
}

// CleanupErrorLogsBefore 删除指定时间之前的错误日志，返回删除条数。
func (s *Store) CleanupErrorLogsBefore(beforeMs int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM error_logs WHERE created_at < ?`, beforeMs)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
