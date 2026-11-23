package store

import (
	"testing"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// 渠道快照缓存必须在写操作后失效，否则面板改完渠道要重启才生效。
func TestChannelCacheInvalidatedOnWrite(t *testing.T) {
	s := openTestStore(t)

	ch := &model.Channel{
		ID:        "ch-1",
		Name:      "测试渠道",
		BaseURL:   "https://example.com/v1",
		Enabled:   true,
		Models:    []string{"m1"},
		SortOrder: 1,
	}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}

	first, err := s.ListChannels()
	if err != nil {
		t.Fatalf("首次读取失败: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("期望 1 个渠道，实际 %d", len(first))
	}

	// 命中缓存时返回同一份快照
	second, err := s.ListChannels()
	if err != nil {
		t.Fatalf("再次读取失败: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("缓存读取期望 1 个渠道，实际 %d", len(second))
	}

	ch.Name = "改名后的渠道"
	if err := s.UpdateChannel(ch); err != nil {
		t.Fatalf("更新渠道失败: %v", err)
	}
	after, err := s.ListChannels()
	if err != nil {
		t.Fatalf("更新后读取失败: %v", err)
	}
	if len(after) != 1 || after[0].Name != "改名后的渠道" {
		t.Fatalf("更新后应读到新名称，实际 %+v", after)
	}

	if err := s.DeleteChannel(ch.ID); err != nil {
		t.Fatalf("删除渠道失败: %v", err)
	}
	final, err := s.ListChannels()
	if err != nil {
		t.Fatalf("删除后读取失败: %v", err)
	}
	if len(final) != 0 {
		t.Fatalf("删除后应为空，实际 %d 个", len(final))
	}
}
