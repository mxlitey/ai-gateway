package store

import (
	"strconv"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// ---------- 请求用量 ----------

const requestLogCols = `id, created_at, api_key_id, api_key_name, channel_id, channel_name,
	endpoint, stream, model, upstream_model, upstream_key_hint, status, success, attempts, duration_ms,
	prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens,
	cost_prompt_usd, cost_completion_usd, cost_usd, price_model, price_source, error`

// 插入时不带 id：INTEGER PRIMARY KEY 显式写入 0 会被当成合法 rowid
const requestLogInsertCols = `created_at, api_key_id, api_key_name, channel_id, channel_name,
	endpoint, stream, model, upstream_model, upstream_key_hint, status, success, attempts, duration_ms,
	prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens,
	cost_prompt_usd, cost_completion_usd, cost_usd, price_model, price_source, error`

func scanRequestLog(sc scanner) (*model.RequestLog, error) {
	r := &model.RequestLog{}
	var stream, success int
	if err := sc.Scan(
		&r.ID, &r.CreatedAt, &r.APIKeyID, &r.APIKeyName, &r.ChannelID, &r.ChannelName,
		&r.Endpoint, &stream, &r.Model, &r.UpstreamModel, &r.UpstreamKeyHint,
		&r.Status, &success, &r.Attempts, &r.DurationMS,
		&r.PromptTokens, &r.CompletionTokens, &r.TotalTokens, &r.CachedTokens, &r.ReasoningTokens,
		&r.CostPromptUSD, &r.CostCompletionUSD, &r.CostUSD,
		&r.PriceModel, &r.PriceSource, &r.Error,
	); err != nil {
		return nil, err
	}
	r.Stream = stream != 0
	r.Success = success != 0
	return r, nil
}

// InsertRequestLog 写入一条用量记录。
func (s *Store) InsertRequestLog(r *model.RequestLog) error {
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().UnixMilli()
	}
	res, err := s.db.Exec(
		`INSERT INTO request_logs (`+requestLogInsertCols+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.CreatedAt, r.APIKeyID, r.APIKeyName, r.ChannelID, r.ChannelName,
		r.Endpoint, boolToInt(r.Stream), r.Model, r.UpstreamModel, r.UpstreamKeyHint,
		r.Status, boolToInt(r.Success), r.Attempts, r.DurationMS,
		r.PromptTokens, r.CompletionTokens, r.TotalTokens, r.CachedTokens, r.ReasoningTokens,
		r.CostPromptUSD, r.CostCompletionUSD, r.CostUSD,
		r.PriceModel, r.PriceSource, truncateForDB(r.Error, 2000),
	)
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil {
		r.ID = id
	}
	return nil
}

// RequestLogFilter 用量明细的查询条件（零值表示不过滤）。
type RequestLogFilter struct {
	FromMs      int64
	ToMs        int64
	Model       string
	ChannelID   string
	ChannelName string
	APIKeyID    string
	OnlyFail    bool
	Limit       int
	Offset      int
}

func (f RequestLogFilter) where() (string, []any) {
	conds := []string{"created_at >= ?", "created_at < ?"}
	args := []any{f.FromMs, f.ToMs}
	if f.Model != "" {
		conds = append(conds, "model = ?")
		args = append(args, f.Model)
	}
	if f.ChannelID != "" {
		conds = append(conds, "channel_id = ?")
		args = append(args, f.ChannelID)
	}
	if f.ChannelName != "" {
		conds = append(conds, "channel_name = ?")
		args = append(args, f.ChannelName)
	}
	if f.APIKeyID != "" {
		conds = append(conds, "api_key_id = ?")
		args = append(args, f.APIKeyID)
	}
	if f.OnlyFail {
		conds = append(conds, "success = 0")
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListRequestLogs 按时间倒序返回用量明细。
func (s *Store) ListRequestLogs(f RequestLogFilter) ([]model.RequestLog, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where, args := f.where()
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.Query(
		`SELECT `+requestLogCols+` FROM request_logs`+where+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.RequestLog{}
	for rows.Next() {
		r, err := scanRequestLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// CountRequestLogs 返回符合条件的明细总条数（分页用）。
func (s *Store) CountRequestLogs(f RequestLogFilter) (int64, error) {
	where, args := f.where()
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(1) FROM request_logs`+where, args...).Scan(&n)
	return n, err
}

// usageConds 构造用量区间条件：时间区间 + 可选的渠道名 / 公开模型名筛选。
// 与趋势图、明细的筛选口径一致（都是按渠道名与公开模型名精确匹配）。
func usageConds(fromMs, toMs int64, channelName, modelName string) (string, []any) {
	conds := []string{"created_at >= ?", "created_at < ?"}
	args := []any{fromMs, toMs}
	if channelName != "" {
		conds = append(conds, "channel_name = ?")
		args = append(args, channelName)
	}
	if modelName != "" {
		conds = append(conds, "model = ?")
		args = append(args, modelName)
	}
	return strings.Join(conds, " AND "), args
}

// UsageSummary 汇总时间区间内的用量与费用；channelName / modelName 为空表示不筛选。
func (s *Store) UsageSummary(fromMs, toMs int64, channelName, modelName string) (*model.UsageSummary, error) {
	sum := &model.UsageSummary{From: fromMs, To: toMs}
	where, args := usageConds(fromMs, toMs, channelName, modelName)

	err := s.db.QueryRow(
		`SELECT COUNT(1),
		        COALESCE(SUM(success), 0),
		        COALESCE(SUM(CASE WHEN price_source = 'none' THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(prompt_tokens), 0),
		        COALESCE(SUM(completion_tokens), 0),
		        COALESCE(SUM(total_tokens), 0),
		        COALESCE(SUM(cached_tokens), 0),
		        COALESCE(SUM(cost_usd), 0)
		 FROM request_logs WHERE `+where,
		args...,
	).Scan(
		&sum.Requests, &sum.SuccessRequests, &sum.UnpricedRequests,
		&sum.PromptTokens, &sum.CompletionTokens, &sum.TotalTokens, &sum.CachedTokens, &sum.CostUSD,
	)
	if err != nil {
		return nil, err
	}
	sum.FailedRequests = sum.Requests - sum.SuccessRequests

	// 分组结果不受「自身」筛选影响：界面用它填充筛选下拉框，若跟着自身筛选走，
	// 选中某个值后其余选项就会从下拉框里消失。例外：按模型的列表要跟随渠道筛选，
	// 这样选定渠道后模型下拉只展示该渠道用过的模型（但不跟随模型筛选，保证当前选中项仍在列表里）。
	whereAll, argsAll := usageConds(fromMs, toMs, "", "")
	whereModel, argsModel := usageConds(fromMs, toMs, channelName, "")
	group := func(expr, label, where string, args []any) ([]model.UsageBucket, error) {
		rows, err := s.db.Query(
			`SELECT `+expr+` AS k, COUNT(1),
			        COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0),
			        COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cached_tokens), 0),
			        COALESCE(SUM(cost_usd), 0)
			 FROM request_logs WHERE `+where+`
			 GROUP BY k ORDER BY 7 DESC`,
			args...,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		out := []model.UsageBucket{}
		for rows.Next() {
			b := model.UsageBucket{}
			if err := rows.Scan(&b.Key, &b.Requests, &b.PromptTokens, &b.CompletionTokens, &b.TotalTokens, &b.CachedTokens, &b.CostUSD); err != nil {
				return nil, err
			}
			if b.Key == "" {
				b.Key = label
			}
			out = append(out, b)
		}
		return out, rows.Err()
	}

	if sum.ByModel, err = group("model", "(未指定模型)", whereModel, argsModel); err != nil {
		return nil, err
	}
	if sum.ByChannel, err = group("channel_name", "(未命中渠道)", whereAll, argsAll); err != nil {
		return nil, err
	}
	if sum.ByAPIKey, err = group("api_key_name", "(未知密钥)", whereAll, argsAll); err != nil {
		return nil, err
	}
	// 按北京时区自然日聚合，并按日期升序（便于画趋势）
	sum.ByDay, err = group(`strftime('%Y-%m-%d', created_at / 1000, 'unixepoch', '+8 hours')`, "", whereAll, argsAll)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(sum.ByDay)-1; i < j; i, j = i+1, j-1 {
		sum.ByDay[i], sum.ByDay[j] = sum.ByDay[j], sum.ByDay[i]
	}
	return sum, nil
}

// 趋势图的时间桶粒度。
const (
	UsageBucketHour = "hour"
	UsageBucketDay  = "day"
)

// UsageSeries 聚合「最近 count 个」小时 / 天时间桶的用量（趋势图用）；
// channelName / modelName 为空表示不筛选。返回的桶按时间升序，并包含区间内没有请求的空桶，
// 同时给出整体区间 [fromMs, toMs)。
func (s *Store) UsageSeries(toMs int64, count int, bucket, channelName, modelName string) ([]model.UsageBucket, int64, int64, error) {
	if count <= 0 {
		count = 1
	}
	if count > 1000 {
		count = 1000
	}
	starts, fromMs, endMs := bucketStarts(toMs, count, bucket)
	out, err := s.usageSeriesBuckets(starts, fromMs, endMs, bucket, channelName, modelName)
	if err != nil {
		return nil, 0, 0, err
	}
	return out, fromMs, endMs, nil
}

// UsageSeriesRange 聚合调用方给定的 [fromMs, toMs) 区间内的小时 / 天时间桶
// （日历选择器选出的日期 / 小时范围）。语义与 UsageSeries 一致，只是区间由外部指定。
func (s *Store) UsageSeriesRange(fromMs, toMs int64, bucket, channelName, modelName string) ([]model.UsageBucket, int64, int64, error) {
	starts, endMs := rangeBucketStarts(fromMs, toMs, bucket)
	out, err := s.usageSeriesBuckets(starts, fromMs, endMs, bucket, channelName, modelName)
	if err != nil {
		return nil, 0, 0, err
	}
	return out, fromMs, endMs, nil
}

// rangeBucketStarts 按 [fromMs, toMs) 生成时间桶起点（升序，含没有请求的空桶），
// 返回桶起点与实际查询区间右端点（对齐到桶边界）；时区固定北京，与聚合表达式一致。
func rangeBucketStarts(fromMs, toMs int64, bucket string) ([]time.Time, int64) {
	loc := time.FixedZone("CST", 8*60*60)
	step := 24 * time.Hour
	truncate := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	if bucket == UsageBucketHour {
		step = time.Hour
		truncate = func(t time.Time) time.Time {
			return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc)
		}
	}

	start := truncate(time.UnixMilli(fromMs).In(loc))
	// toMs 为开区间，最后一个桶由「区间内最后一毫秒」所在的时间桶决定
	end := truncate(time.UnixMilli(toMs - 1).In(loc)).Add(step)
	if !end.After(start) {
		end = start.Add(step)
	}

	starts := []time.Time{}
	for t := start; t.Before(end) && len(starts) < 1000; t = t.Add(step) {
		starts = append(starts, t)
	}
	return starts, end.UnixMilli()
}

// usageSeriesBuckets 按给定桶起点查询聚合结果，补齐没有请求的空桶。
func (s *Store) usageSeriesBuckets(starts []time.Time, fromMs, endMs int64, bucket, channelName, modelName string) ([]model.UsageBucket, error) {
	expr := `strftime('%Y-%m-%d', created_at / 1000, 'unixepoch', '+8 hours')`
	layout := "2006-01-02"
	if bucket == UsageBucketHour {
		expr = `strftime('%Y-%m-%d %H:00', created_at / 1000, 'unixepoch', '+8 hours')`
		layout = "2006-01-02 15:00"
	}
	where, args := usageConds(fromMs, endMs, channelName, modelName)

	rows, err := s.db.Query(
		`SELECT `+expr+` AS k, COUNT(1),
		        COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0),
		        COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cached_tokens), 0),
		        COALESCE(SUM(cost_usd), 0)
		 FROM request_logs WHERE `+where+`
		 GROUP BY k ORDER BY k ASC`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byKey := map[string]model.UsageBucket{}
	for rows.Next() {
		b := model.UsageBucket{}
		if err := rows.Scan(&b.Key, &b.Requests, &b.PromptTokens, &b.CompletionTokens, &b.TotalTokens, &b.CachedTokens, &b.CostUSD); err != nil {
			return nil, err
		}
		byKey[b.Key] = b
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]model.UsageBucket, 0, len(starts))
	for _, t := range starts {
		key := t.Format(layout)
		if b, ok := byKey[key]; ok {
			out = append(out, b)
			continue
		}
		out = append(out, model.UsageBucket{Key: key})
	}
	return out, nil
}

// bucketStarts 返回「最近 count 个」时间桶的起始时刻（升序，最后一个为当前所处的时间桶），
// 以及整体查询区间 [fromMs, toMs)。时区固定为北京（与上面的 strftime 表达式一致）。
func bucketStarts(toMs int64, count int, bucket string) ([]time.Time, int64, int64) {
	loc := time.FixedZone("CST", 8*60*60)
	cur := time.UnixMilli(toMs).In(loc)
	step := 24 * time.Hour
	if bucket == UsageBucketHour {
		cur = time.Date(cur.Year(), cur.Month(), cur.Day(), cur.Hour(), 0, 0, 0, loc)
		step = time.Hour
	} else {
		cur = time.Date(cur.Year(), cur.Month(), cur.Day(), 0, 0, 0, 0, loc)
	}

	starts := make([]time.Time, count)
	for i := range starts {
		starts[count-1-i] = cur.Add(-time.Duration(i) * step)
	}
	return starts, starts[0].UnixMilli(), cur.Add(step).UnixMilli()
}

// CleanupRequestLogsBefore 删除指定时间之前的用量明细，返回删除条数。
func (s *Store) CleanupRequestLogsBefore(beforeMs int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM request_logs WHERE created_at < ?`, beforeMs)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}

// ---------- 模型价格 ----------

const modelPriceCols = `model, display_name, prompt_price, completion_price, cache_read_price, context_length, source, updated_at`

func scanModelPrice(sc scanner) (*model.ModelPrice, error) {
	p := &model.ModelPrice{}
	if err := sc.Scan(
		&p.Model, &p.DisplayName, &p.PromptPrice, &p.CompletionPrice,
		&p.CacheReadPrice, &p.ContextLength, &p.Source, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return p, nil
}

// ListModelPrices 返回全部价格条目（按模型名升序）。
func (s *Store) ListModelPrices() ([]model.ModelPrice, error) {
	rows, err := s.db.Query(`SELECT ` + modelPriceCols + ` FROM model_prices ORDER BY model ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.ModelPrice{}
	for rows.Next() {
		p, err := scanModelPrice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// UpsertModelPrice 写入或覆盖一条价格。
func (s *Store) UpsertModelPrice(p *model.ModelPrice) error {
	if p.UpdatedAt == 0 {
		p.UpdatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.Exec(
		`INSERT INTO model_prices (`+modelPriceCols+`) VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(model) DO UPDATE SET
		   display_name = excluded.display_name,
		   prompt_price = excluded.prompt_price,
		   completion_price = excluded.completion_price,
		   cache_read_price = excluded.cache_read_price,
		   context_length = excluded.context_length,
		   source = excluded.source,
		   updated_at = excluded.updated_at`,
		p.Model, p.DisplayName, p.PromptPrice, p.CompletionPrice,
		p.CacheReadPrice, p.ContextLength, p.Source, p.UpdatedAt,
	)
	return err
}

// DeleteModelPrice 删除一条价格。
func (s *Store) DeleteModelPrice(modelName string) error {
	_, err := s.db.Exec(`DELETE FROM model_prices WHERE model = ?`, modelName)
	return err
}

// ReplacePricesOfSource 以 source 为界整体替换价格（用于 OpenRouter 快照导入）。
// source = 'manual' 的手工条目不受影响。
func (s *Store) ReplacePricesOfSource(source string, prices []model.ModelPrice) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM model_prices WHERE source = ?`, source); err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(
		`INSERT INTO model_prices (` + modelPriceCols + `) VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(model) DO UPDATE SET
		   display_name = excluded.display_name,
		   prompt_price = excluded.prompt_price,
		   completion_price = excluded.completion_price,
		   cache_read_price = excluded.cache_read_price,
		   context_length = excluded.context_length,
		   source = excluded.source,
		   updated_at = excluded.updated_at
		 WHERE model_prices.source <> 'manual'`, // 手工维护的价格不被快照覆盖
	)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now().UnixMilli()
	n := 0
	for i := range prices {
		p := &prices[i]
		if p.UpdatedAt == 0 {
			p.UpdatedAt = now
		}
		if _, err := stmt.Exec(
			p.Model, p.DisplayName, p.PromptPrice, p.CompletionPrice,
			p.CacheReadPrice, p.ContextLength, p.Source, p.UpdatedAt,
		); err != nil {
			return 0, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// CountModelPrices 返回价格条目数（用于面板展示覆盖情况）。
func (s *Store) CountModelPrices() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(1) FROM model_prices`).Scan(&n)
	return n, err
}

// CleanupIfDueSetting 计算「距上次执行是否已超过 interval」。
func (s *Store) DueSince(key string, interval time.Duration) bool {
	v, ok, err := s.GetSetting(key)
	if err != nil || !ok {
		return true
	}
	last, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return true
	}
	return time.Now().UnixMilli()-last >= interval.Milliseconds()
}

// MarkDone 记录某后台任务最近一次执行时间。
func (s *Store) MarkDone(key string) error {
	return s.SetSetting(key, strconv.FormatInt(time.Now().UnixMilli(), 10))
}

func truncateForDB(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}