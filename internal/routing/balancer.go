// Package routing 负责目标（渠道 + 密钥 + 上游模型）选择。
package routing

import (
	"sync"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// Target 一次可尝试的转发目标。
type Target struct {
	Channel       *model.Channel
	Key           string
	UpstreamModel string
	PublicModel   string
}

// candidate 参与排序的候选渠道。
type candidate struct {
	ch       *model.Channel
	upstream string
	priority int
}

// Balancer 负载均衡器。
type Balancer struct {
	store *store.Store

	mu       sync.Mutex
	counters map[string]uint64 // 渠道 id -> 轮询计数（决定每次的首选密钥）
}

// New 创建负载均衡器。
func New(s *store.Store) *Balancer {
	return &Balancer{store: s, counters: map[string]uint64{}}
}

// nextCounter 取某渠道的下一个轮询序号（并发安全）。
func (b *Balancer) nextCounter(channelID string) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := b.counters[channelID]
	b.counters[channelID] = v + 1
	return v
}

// SelectTarget 按公开模型名解析有序目标列表。
//
// 规则：
//  1. 跳过停用渠道、无启用密钥的渠道、以及不在 allowedChannelIDs 内的渠道
//  2. 上游模型名优先取 model_map，未命中回退 models 同名透传
//  3. 按 model_priority 降序，相同优先级保持渠道存储顺序（稳定）
//  4. 渠道内密钥按 key_strategy 展开（轮询=按渠道计数器依次轮换，故障转移=保持配置顺序），
//     按 channelId:key:上游模型 去重
//
// allowedChannelIDs 为空表示不限制。返回空切片表示无可用目标。
func (b *Balancer) SelectTarget(publicModel string, allowedChannelIDs []string) ([]Target, error) {
	channels, err := b.store.ListChannels()
	if err != nil {
		return nil, err
	}

	var allowed map[string]bool
	if len(allowedChannelIDs) > 0 {
		allowed = make(map[string]bool, len(allowedChannelIDs))
		for _, cid := range allowedChannelIDs {
			allowed[cid] = true
		}
	}

	rows := make([]candidate, 0, len(channels))
	for i := range channels {
		ch := &channels[i]
		if !ch.Enabled {
			continue
		}
		if len(ch.EnabledKeys()) == 0 {
			continue
		}
		if allowed != nil && !allowed[ch.ID] {
			continue
		}
		um, ok := ch.ResolveUpstreamModel(publicModel)
		if !ok {
			continue
		}
		if ch.IsModelDisabled(publicModel) {
			continue
		}
		rows = append(rows, candidate{
			ch:       ch,
			upstream: um,
			priority: ch.Priority(publicModel),
		})
	}

	if len(rows) == 0 {
		return nil, nil
	}

	// 稳定排序：优先级降序，相同则维持原顺序
	sortByPriorityDesc(rows)

	targets := make([]Target, 0, len(rows))
	seen := map[string]bool{}
	for _, r := range rows {
		for _, key := range b.orderedKeys(r.ch) {
			dedupe := r.ch.ID + ":" + key + ":" + r.upstream
			if seen[dedupe] {
				continue
			}
			seen[dedupe] = true
			targets = append(targets, Target{
				Channel:       r.ch,
				Key:           key,
				UpstreamModel: r.upstream,
				PublicModel:   publicModel,
			})
		}
	}
	return targets, nil
}

// sortByPriorityDesc 依据 priority 降序做稳定排序（插入排序，规模小且天然稳定）。
func sortByPriorityDesc(rows []candidate) {
	for i := 1; i < len(rows); i++ {
		cur := rows[i]
		j := i - 1
		for j >= 0 && rows[j].priority < cur.priority {
			rows[j+1] = rows[j]
			j--
		}
		rows[j+1] = cur
	}
}

// orderedKeys 按渠道的密钥选用策略展开可用密钥。
//
//   - 故障转移（ordered）：保持配置顺序，前一个失败才用下一个；
//   - 轮询（rotate，默认）：按渠道计数器依次轮换首选密钥（K1→K2→K3→K1…），把流量均衡分摊到各密钥。
func (b *Balancer) orderedKeys(ch *model.Channel) []string {
	keys := ch.EnabledKeys()
	n := len(keys)
	if n == 0 {
		return nil
	}
	if ch.KeyStrategy == model.KeyStrategyOrdered {
		return keys
	}
	start := int(b.nextCounter(ch.ID) % uint64(n))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, keys[(start+i)%n])
	}
	return out
}