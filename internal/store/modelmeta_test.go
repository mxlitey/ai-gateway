package store

import (
	"testing"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// TestModelMetaRoundTrip 保存后能原样读回，覆盖所有字段。
func TestModelMetaRoundTrip(t *testing.T) {
	s := openTestStore(t)

	in := &model.ModelMeta{
		Model:          "Qwen-Image-2.1-Turbo",
		Description:    "通用文生图",
		Reference:      true,
		NegativePrompt: true,
		Seed:           true,
		ChinesePrompt:  true,
		ChineseText:    false,
		MaxRefCount:    4,
		MaxN:           4,
		MaxNRef:        1,
		OutputFormats:  []string{"png", "jpeg"},
		MinWidthPx:     512,
		MinHeightPx:    768,
		MaxWidthPx:     4096,
		MaxHeightPx:    6144,
		SizeMultiple:   16,
	}
	if err := s.UpsertModelMeta(in); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetModelMeta(in.Model)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("应能读回刚保存的记录")
	}
	if got.Model != in.Model || got.Description != in.Description {
		t.Fatalf("基本字段不符: %+v", got)
	}
	if !got.Reference || !got.Seed || got.ChineseText {
		t.Fatalf("能力开关不符: %+v", got)
	}
	if got.MaxRefCount != 4 {
		t.Fatalf("参考图张数不符: %+v", got)
	}
	if len(got.OutputFormats) != 2 || got.OutputFormats[0] != "png" {
		t.Fatalf("输出格式不符: %v", got.OutputFormats)
	}
	if got.MaxN != 4 || got.MaxNRef != 1 {
		t.Fatalf("张数不符: %+v", got)
	}
	if got.MinWidthPx != 512 || got.MinHeightPx != 768 ||
		got.MaxWidthPx != 4096 || got.MaxHeightPx != 6144 {
		t.Fatalf("分辨率区间不符: %+v", got)
	}
	if got.SizeMultiple != 16 {
		t.Fatalf("尺寸倍数不符: %+v", got)
	}
	if got.UpdatedAt == 0 {
		t.Fatal("应写入保存时间")
	}
}

// TestModelMetaUpsertOverwrites 同名再存应整体覆盖，而不是合并。
func TestModelMetaUpsertOverwrites(t *testing.T) {
	s := openTestStore(t)
	if err := s.UpsertModelMeta(&model.ModelMeta{
		Model: "m", Description: "旧", MaxN: 4, OutputFormats: []string{"png"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertModelMeta(&model.ModelMeta{
		Model: "m", Description: "新",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetModelMeta("m")
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.Description != "新" {
		t.Fatalf("说明应被覆盖，实际 %q", got.Description)
	}
	if got.MaxN != 0 || len(got.OutputFormats) != 0 {
		t.Fatalf("未提交的字段应被清空（整体覆盖），实际 %+v", got)
	}
}

// TestModelMetaListAndDelete 列表与删除。
func TestModelMetaListAndDelete(t *testing.T) {
	s := openTestStore(t)
	for _, name := range []string{"b-model", "a-model"} {
		if err := s.UpsertModelMeta(&model.ModelMeta{Model: name}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListModelMetas()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Model != "a-model" {
		t.Fatalf("应按模型名升序返回，实际 %+v", list)
	}

	if err := s.DeleteModelMeta("a-model"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetModelMeta("a-model")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("删除后应查不到，实际 %+v", got)
	}
}
