// Package penalty 负责上游密钥的失败冷却：请求上游失败即冷却该密钥，
// 冷却期内不再参与路由；连续失败按倍数递增冷却时长，任意一次成功即重置。
package penalty

import (
	"log/slog"
	"sync"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// Config 冷却规则参数。
type Config struct {
	// Enabled 总开关；关闭时所有方法与未启用一致，不做任何冷却。
	Enabled bool
	// Base 首次失败的冷却时长。
	Base time.Duration
	// Max 单次冷却时长上限。
	Max time.Duration
	// Factor 连续失败时冷却时长的递增倍数。
	Factor int
}

// DefaultConfig 返回默认规则：初始 60s、3 倍递增、上限 30 分钟。
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		Base:    60 * time.Second,
		Max:     30 * time.Minute,
		Factor:  3,
	}
}

// cooldownFor 计算第 streak 次（从 1 计）连续失败的冷却时长。
// 用累乘并在触顶时提前退出，避免 2^n / factor^n 溢出。
func (c Config) cooldownFor(streak int) time.Duration {
	if streak <= 1 {
		return c.clamp(c.Base)
	}
	d := c.Base
	for i := 1; i < streak; i++ {
		d *= time.Duration(c.Factor)
		if d >= c.Max {
			return c.Max
		}
	}
	return c.clamp(d)
}

func (c Config) clamp(d time.Duration) time.Duration {
	if d > c.Max {
		return c.Max
	}
	if d < 0 {
		return 0
	}
	return d
}

// entry 内存中的冷却记录。
type entry struct {
	streak int
	until  time.Time
}

// Cooler 上游密钥冷却器：内存状态 + 异步落库。
//
// 冷却判定走内存（无锁读、微秒级），写入异步落库，不给转发路径加数据库延迟。
// Cooler 可以被多个请求并发热点调用；nil 接收者安全（视为未启用）。
type Cooler struct {
	store *store.Store

	mu      sync.Mutex
	entries map[string]*entry // key: channelID + "\x00" + keyHint
}

// New 创建冷却器并从数据库载入未过期的冷却状态（重启后冷却继续生效）。
// s 为 nil 时仅使用内存（不持久化）。
func New(s *store.Store, cfg Config) *Cooler {
	c := &Cooler{store: s, entries: map[string]*entry{}}
	_ = cfg
	if s == nil {
		return c
	}
	rows, err := s.LoadKeyCooldowns()
	if err != nil {
		slog.Error("[penalty] 载入密钥冷却状态失败", "err", err)
		return c
	}
	now := time.Now()
	for _, r := range rows {
		if r.CooldownUntil <= now.UnixMilli() {
			continue
		}
		c.entries[ckey(r.ChannelID, r.KeyHint)] = &entry{
			streak: r.FailStreak,
			until:  time.UnixMilli(r.CooldownUntil),
		}
	}
	if n := len(c.entries); n > 0 {
		slog.Info("[penalty] 已恢复密钥冷却状态", "count", n)
	}
	return c
}

// Cooling 判断某密钥当前是否处于冷却中。
func (c *Cooler) Cooling(channelID, key string) bool {
	if c == nil {
		return false
	}
	kh := model.KeyHint(key)
	c.mu.Lock()
	e, ok := c.entries[ckey(channelID, kh)]
	c.mu.Unlock()
	if !ok {
		return false
	}
	return time.Now().Before(e.until)
}

// Fail 记录一次上游失败：累加连续失败次数并按倍数递增冷却时长。
func (c *Cooler) Fail(channelID, key string, cfg Config, status int) {
	if c == nil || !cfg.Enabled {
		return
	}
	if key == "" {
		return
	}
	kh := model.KeyHint(key)
	k := ckey(channelID, kh)
	now := time.Now()

	c.mu.Lock()
	e, ok := c.entries[k]
	if !ok {
		e = &entry{}
		c.entries[k] = e
	}
	e.streak++
	cd := cfg.cooldownFor(e.streak)
	e.until = now.Add(cd)
	streak := e.streak
	c.mu.Unlock()

	slog.Warn("[penalty] 上游密钥失败，进入冷却",
		"channel", channelID, "key", kh, "status", status,
		"streak", streak, "cooldown", cd.String())

	c.persist(store.KeyCooldown{
		ChannelID:     channelID,
		KeyHint:       kh,
		FailStreak:    streak,
		CooldownUntil: e.until.UnixMilli(),
		UpdatedAt:     now.UnixMilli(),
	})
}

// Success 记录一次成功：清零连续失败并解除冷却。
func (c *Cooler) Success(channelID, key string) {
	if c == nil || key == "" {
		return
	}
	kh := model.KeyHint(key)
	k := ckey(channelID, kh)

	c.mu.Lock()
	_, ok := c.entries[k]
	if ok {
		delete(c.entries, k)
	}
	c.mu.Unlock()

	// 只有确实存在记录时才需要清理，避免成功路径无谓写库
	if !ok {
		return
	}
	if c.store != nil {
		go func() {
			if err := c.store.ClearKeyCooldown(channelID, kh); err != nil {
				slog.Error("[penalty] 清除密钥冷却失败", "err", err)
			}
		}()
	}
}

// Reset 手动重置某条冷却（面板操作），立即生效并清理落库记录。
func (c *Cooler) Reset(channelID, keyHint string) {
	if c == nil {
		return
	}
	k := ckey(channelID, keyHint)
	c.mu.Lock()
	delete(c.entries, k)
	c.mu.Unlock()

	if c.store != nil {
		if err := c.store.ClearKeyCooldown(channelID, keyHint); err != nil {
			slog.Error("[penalty] 重置密钥冷却失败", "err", err)
		}
	}
}

// persist 异步入库，避免阻塞转发路径。
func (c *Cooler) persist(row store.KeyCooldown) {
	if c.store == nil {
		return
	}
	go func() {
		if err := c.store.UpsertKeyCooldown(row); err != nil {
			slog.Error("[penalty] 写入密钥冷却状态失败", "err", err)
		}
	}()
}

func ckey(channelID, keyHint string) string {
	return channelID + "\x00" + keyHint
}
