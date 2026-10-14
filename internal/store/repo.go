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

const channelCols = `id, name, base_url, path, keys, models, model_map, model_priority, headers, key_strategy, enabled, source, sort_order, created_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanChannel(sc scanner) (*model.Channel, error) {
	c := &model.Channel{
		Keys:          []model.ChannelKey{},
		Models:        []string{},
		ModelMap:      map[string]string{},
		ModelPriority: map[string]int{},
		Headers:       []model.Header{},
	}
	var keysS, modelsS, mapS, priS, headersS string
	var enabled int
	if err := sc.Scan(
		&c.ID, &c.Name, &c.BaseURL, &c.Path,
		&keysS, &modelsS, &mapS, &priS, &headersS,
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
	if err := decodeJSON(headersS, &c.Headers); err != nil {
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
	if c.Headers == nil {
		c.Headers = []model.Header{}
	}
	return c, nil
}

// ListChannels 按展示顺序返回全部渠道。
func (s *Store) ListChannels() ([]model.Channel, error) {
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
	return out, rows.Err()
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
	hd, err := marshalJSON(c.Headers)
	if err != nil {
		return err
	}
	c.KeyStrategy = model.NormalizeKeyStrategy(c.KeyStrategy)
	_, err = s.db.Exec(
		`INSERT INTO channels
		 (id, name, base_url, path, keys, models, model_map, model_priority, headers, key_strategy, enabled, source, sort_order, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.BaseURL, c.Path, keys, models, mm, mp, hd,
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
	hd, err := marshalJSON(c.Headers)
	if err != nil {
		return err
	}
	c.KeyStrategy = model.NormalizeKeyStrategy(c.KeyStrategy)
	_, err = s.db.Exec(
		`UPDATE channels SET
		   name = ?, base_url = ?, path = ?, keys = ?, models = ?,
		   model_map = ?, model_priority = ?, headers = ?, key_strategy = ?, enabled = ?
		 WHERE id = ?`,
		c.Name, c.BaseURL, c.Path, keys, models, mm, mp, hd, c.KeyStrategy, boolToInt(c.Enabled), c.ID,
	)
	return err
}

// DeleteChannel 删除渠道。
func (s *Store) DeleteChannel(id string) error {
	_, err := s.db.Exec(`DELETE FROM channels WHERE id = ?`, id)
	return err
}

// ---------- 客户端密钥 ----------

const apiKeyCols = `id, name, key, channel_ids, enabled, created_at`

func scanAPIKey(sc scanner) (*model.APIKey, error) {
	k := &model.APIKey{ChannelIDs: []string{}}
	var chS string
	var enabled int
	if err := sc.Scan(&k.ID, &k.Name, &k.Key, &chS, &enabled, &k.CreatedAt); err != nil {
		return nil, err
	}
	k.Enabled = enabled != 0
	if err := decodeJSON(chS, &k.ChannelIDs); err != nil {
		return nil, err
	}
	if k.ChannelIDs == nil {
		k.ChannelIDs = []string{}
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
	_, err = s.db.Exec(
		`INSERT INTO api_keys (id, name, key, channel_ids, enabled, created_at) VALUES (?,?,?,?,?,?)`,
		k.ID, k.Name, k.Key, ch, boolToInt(k.Enabled), k.CreatedAt,
	)
	return err
}

// UpdateAPIKey 更新密钥（名称 / 绑定渠道 / 启停）。
func (s *Store) UpdateAPIKey(k *model.APIKey) error {
	ch, err := marshalJSON(k.ChannelIDs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE api_keys SET name = ?, channel_ids = ?, enabled = ? WHERE id = ?`,
		k.Name, ch, boolToInt(k.Enabled), k.ID,
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
		out = append(out, e)
	}
	return out, rows.Err()
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