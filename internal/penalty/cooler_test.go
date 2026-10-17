package penalty

import (
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

func newMemory(cfg Config) *Cooler { return New(nil, cfg) }

// streakFor 读取某密钥当前的连续失败计数（仅供测试使用）。
func (c *Cooler) streakFor(channelID, key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[ckey(channelID, model.KeyHint(key))]
	if !ok {
		return 0
	}
	return e.streak
}

// TestCooldownSequence 覆盖「3 倍递增、5 次内打满上限」的核心规则。
func TestCooldownSequence(t *testing.T) {
	cfg := Config{Enabled: true, Base: 60 * time.Second, Max: 30 * time.Minute, Factor: 3}
	want := []time.Duration{
		60 * time.Second,   // 1
		180 * time.Second,  // 2
		540 * time.Second,  // 3
		1620 * time.Second, // 4
		1800 * time.Second, // 5（触顶）
		1800 * time.Second, // 6
		1800 * time.Second, // 7
	}
	for i, w := range want {
		got := cfg.cooldownFor(i + 1)
		if got != w {
			t.Fatalf("streak=%d: got %v, want %v", i+1, got, w)
		}
	}
}

// TestCooldownNoOverflow 连续失败很多次也不会溢出，保持在上限。
func TestCooldownNoOverflow(t *testing.T) {
	cfg := Config{Enabled: true, Base: 60 * time.Second, Max: 30 * time.Minute, Factor: 3}
	for _, streak := range []int{10, 50, 1000} {
		if got := cfg.cooldownFor(streak); got != cfg.Max {
			t.Fatalf("streak=%d: got %v, want %v", streak, got, cfg.Max)
		}
	}
}

// TestSuccessResetsStreak 冷却器层面：成功后续失败从初始值重新开始。
func TestSuccessResetsStreak(t *testing.T) {
	c := newMemory(Config{Enabled: true, Base: 60 * time.Second, Max: 30 * time.Minute, Factor: 3})
	cfg := Config{Enabled: true, Base: 60 * time.Second, Max: 30 * time.Minute, Factor: 3}

	c.Fail("ch1", "sk-abcdefghijklmn", cfg, 429) // streak=1 → 60s
	if !c.Cooling("ch1", "sk-abcdefghijklmn") {
		t.Fatal("失败后应处于冷却中")
	}
	c.Success("ch1", "sk-abcdefghijklmn")
	if c.Cooling("ch1", "sk-abcdefghijklmn") {
		t.Fatal("成功后应解除冷却")
	}
	// 再次失败：streak 应为 1（60s），而非延续之前的计数
	c.Fail("ch1", "sk-abcdefghijklmn", cfg, 429)
	got := c.streakFor("ch1", "sk-abcdefghijklmn")
	if got != 1 {
		t.Fatalf("成功应清零连续失败计数，got streak=%d, want 1", got)
	}
}

// TestDisabled 总开关关闭时不做任何冷却。
func TestDisabled(t *testing.T) {
	c := newMemory(Config{})
	off := Config{Enabled: false, Base: 60 * time.Second, Max: 30 * time.Minute, Factor: 3}
	c.Fail("ch1", "sk-abcdefghijklmn", off, 500)
	if c.Cooling("ch1", "sk-abcdefghijklmn") {
		t.Fatal("总开关关闭时不应进入冷却")
	}
}
