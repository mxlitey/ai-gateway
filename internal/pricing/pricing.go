// Package pricing 提供价格表与费用估算：价格完全由用户在面板中维护，
// 只按公开模型名匹配，用于与各厂商官方价格做对比。
//
// 价格单位：token 类为「每百万 token 的单价」，生图为「每次请求的单价」。
// 数值不带币种含义——用户按自己当地货币填数，展示时由面板的货币符号决定。
// 计费在写用量时一次性算出并落库（费用快照），之后改价不影响历史记录。
package pricing

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// SourceManual / SourceNone 价格来源标记，写入用量记录后用于区分「已定价」与「未定价」。
const (
	SourceManual = "manual"
	SourceNone   = "none"
)

// 峰谷时段使用的时区：与面板、用量统计保持北京时间一致。
var priceLocation = time.FixedZone("CST", 8*60*60)

// Resolver 价格解析器：内存索引 + 落库价格表。
type Resolver struct {
	store *store.Store

	mu    sync.RWMutex
	index map[string]model.ModelPrice
	count int
}

// New 创建解析器（需随后调用 Reload 载入索引）。
func New(s *store.Store) *Resolver {
	return &Resolver{store: s, index: map[string]model.ModelPrice{}}
}

// Reload 从数据库重建内存索引。
func (r *Resolver) Reload() error {
	rows, err := r.store.ListModelPrices()
	if err != nil {
		return err
	}

	idx := buildIndex(rows)

	r.mu.Lock()
	r.index = idx
	r.count = len(rows)
	r.mu.Unlock()
	return nil
}

// buildIndex 用价格表构建匹配索引：公开模型名为主键，
// 另建「去掉厂商前缀」的别名，便于用短名也能命中；
// 别名与某个真实模型名相同时不覆盖真实条目，避免张冠李戴。
func buildIndex(rows []model.ModelPrice) map[string]model.ModelPrice {
	idx := make(map[string]model.ModelPrice, len(rows)*2)
	for i := range rows {
		p := rows[i]
		key := Normalize(p.Model)
		if key == "" {
			continue
		}
		p.Model = key
		idx[key] = p
	}
	for i := range rows {
		p := rows[i]
		key := Normalize(p.Model)
		short := shortName(key)
		if key == "" || short == key {
			continue
		}
		if _, exists := idx[short]; !exists {
			p.Model = key
			idx[short] = p
		}
	}
	return idx
}

// Count 返回当前价格表中的条目数。
func (r *Resolver) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.count
}

// Resolve 解析模型价格，第二返回值为是否命中。
func (r *Resolver) Resolve(modelName string) (model.ModelPrice, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, key := range candidates(modelName) {
		if p, ok := r.index[key]; ok {
			return p, true
		}
	}
	return model.ModelPrice{}, false
}

// ---------- 计价 ----------

// Usage 一次调用的计费输入。
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	// At 计费时刻，用于判定峰谷；零值按当前时间。
	At time.Time
}

// Quote 一次调用按价格表算出的费用。
type Quote struct {
	Total      float64
	Input      float64
	Output     float64
	Factor     float64 // 峰谷系数，1 表示无调整
	TierUpTo   int     // 命中的输入档位上限，0 表示兜底档
	Unit       string
	PriceModel string
}

// Estimate 按价格与用量算出费用。
//
// 判定顺序：按 token 计费时先按「未缓存的输入 token」选档（缓存命中按单列的缓存价，
// 不参与选档），再对整单乘上峰谷系数；按请求次数计费时不看 token，直接按次单价结算。
// 峰谷时段取第一个命中的规则，未命中则系数为 1。
func Estimate(p model.ModelPrice, u Usage) Quote {
	at := u.At
	if at.IsZero() {
		at = time.Now()
	}
	factor := peakFactor(p.TimeWindows, at)

	q := Quote{Factor: factor, Unit: p.Unit, PriceModel: p.Model}

	// 一次调用就是一次请求，按次计费不区分输入输出与张数
	if p.Unit == model.PriceUnitRequest {
		q.Output = p.RequestPrice * factor
		q.Total = q.Output
		return q
	}

	cached := u.CachedTokens
	if cached < 0 {
		cached = 0
	}
	if cached > u.PromptTokens {
		cached = u.PromptTokens
	}
	fresh := u.PromptTokens - cached

	tier := pickTier(p.Tiers, fresh)
	q.TierUpTo = tier.UpTo
	cachePrice := p.CacheReadPrice
	if cachePrice <= 0 {
		cachePrice = tier.PromptPrice
	}

	q.Input = (float64(fresh)*tier.PromptPrice + float64(cached)*cachePrice) / 1e6 * factor
	q.Output = float64(maxInt(u.CompletionTokens, 0)) * tier.CompletionPrice / 1e6 * factor
	q.Total = q.Input + q.Output
	return q
}

// pickTier 选出输入 token 所属的档位：第一档 UpTo 足够大或为 0 的兜底档；
// 没有任何档位时按 0 价处理。
func pickTier(tiers []model.PriceTier, inputTokens int) model.PriceTier {
	for _, t := range tiers {
		if t.UpTo <= 0 || inputTokens <= t.UpTo {
			return t
		}
	}
	return model.PriceTier{}
}

// peakFactor 返回 at 时刻适用的时段系数；没有命中任何窗口时返回 1。
func peakFactor(windows []model.TimeWindow, at time.Time) float64 {
	if len(windows) == 0 {
		return 1
	}
	local := at.In(priceLocation)
	cur := local.Hour()*60 + local.Minute()
	weekday := int(local.Weekday()) // 0=周日 … 6=周六
	for _, w := range windows {
		if !dayMatches(w.Days, weekday) {
			continue
		}
		start, okStart := parseClock(w.Start)
		end, okEnd := parseClock(w.End)
		if !okStart || !okEnd || start == end {
			continue
		}
		in := false
		if start < end {
			in = cur >= start && cur < end
		} else {
			// 跨午夜，例如 22:00-06:00
			in = cur >= start || cur < end
		}
		if in {
			if w.Factor <= 0 {
				return 1
			}
			return w.Factor
		}
	}
	return 1
}

// dayMatches 判断星期是否落在窗口限定的范围内；Days 为空表示每天适用。
func dayMatches(days []int, weekday int) bool {
	if len(days) == 0 {
		return true
	}
	for _, d := range days {
		if d == weekday {
			return true
		}
	}
	return false
}

// parseClock 把 "HH:MM" 解析为当天分钟数。
func parseClock(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, false
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil {
		return 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil {
		return 0, false
	}
	if h < 0 || h > 24 || m < 0 || m > 59 || (h == 24 && m != 0) {
		return 0, false
	}
	return h*60 + m, true
}

// ---------- 名称归一化与匹配 ----------

var (
	reDateSuffix = regexp.MustCompile(`-(20\d{2}-?\d{2}-?\d{2})$`)
	reMultiDash  = regexp.MustCompile(`-{2,}`)
)

// Normalize 把模型名归一化为匹配键：小写、统一分隔符、去掉变体与日期后缀。
func Normalize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if i := strings.LastIndex(s, ":"); i >= 0 { // 去掉 :free / :nitro 变体
		s = s[:i]
	}
	if i := strings.Index(s, "@"); i >= 0 { // 去掉 @版本 后缀
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, " ", "-")
	s = reMultiDash.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// shortName 去掉厂商前缀（anthropic/claude-x -> claude-x）。
func shortName(normalized string) string {
	if i := strings.LastIndex(normalized, "/"); i >= 0 {
		return normalized[i+1:]
	}
	return normalized
}

// candidates 生成按优先级排列的候选匹配键。
func candidates(modelName string) []string {
	n := Normalize(modelName)
	if n == "" {
		return nil
	}
	out := []string{n}
	if short := shortName(n); short != n {
		out = append(out, short)
	}
	if t := reDateSuffix.ReplaceAllString(n, ""); t != n && t != "" {
		out = append(out, t)
		if short := shortName(t); short != t && short != "" {
			out = append(out, short)
		}
	}
	if t := strings.TrimSuffix(n, "-latest"); t != n {
		out = append(out, t)
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
