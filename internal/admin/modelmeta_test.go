package admin

import (
	"testing"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

func TestSanitizeModelMetaClampsNumbers(t *testing.T) {
	got := sanitizeModelMeta("m", model.ModelMeta{
		MaxRefCount:  -5,
		MaxRefSidePx: 99999,
		MaxRefMB:     -1,
		MaxN:         999,
		MaxNRef:      -8,
		DefaultN:     3,
	})
	if got.MaxRefCount != 0 {
		t.Fatalf("负数应归零，实际 %d", got.MaxRefCount)
	}
	if got.MaxRefSidePx != 16384 {
		t.Fatalf("超上限应截断到 16384，实际 %d", got.MaxRefSidePx)
	}
	if got.MaxRefMB != 0 {
		t.Fatalf("负数应归零，实际 %d", got.MaxRefMB)
	}
	if got.MaxN != 16 {
		t.Fatalf("超上限应截断到 16，实际 %d", got.MaxN)
	}
	if got.MaxNRef != 0 {
		t.Fatalf("负数应归零，实际 %d", got.MaxNRef)
	}
	if got.DefaultN != 3 {
		t.Fatalf("合法值不应改动，实际 %d", got.DefaultN)
	}
}

func TestSanitizeModelMetaFiltersFormatsAndSizes(t *testing.T) {
	got := sanitizeModelMeta("m", model.ModelMeta{
		RefFormats: []string{"PNG", " gif ", "webp", "png", "jpeg"},
		Sizes:      []string{"1024x1024", "1024*768", " 512X512 ", "abc", "1x1", "1024x1024"},
	})
	wantFormats := []string{"png", "webp", "jpeg"}
	if len(got.RefFormats) != len(wantFormats) {
		t.Fatalf("格式过滤后应剩 %d 项，实际 %v", len(wantFormats), got.RefFormats)
	}
	for i, f := range wantFormats {
		if got.RefFormats[i] != f {
			t.Fatalf("第 %d 项应为 %s，实际 %s", i, f, got.RefFormats[i])
		}
	}

	wantSizes := []string{"1024x1024", "1024x768", "512x512"}
	if len(got.Sizes) != len(wantSizes) {
		t.Fatalf("尺寸过滤后应剩 %d 项，实际 %v", len(wantSizes), got.Sizes)
	}
	for i, s := range wantSizes {
		if got.Sizes[i] != s {
			t.Fatalf("第 %d 项应为 %s，实际 %s", i, s, got.Sizes[i])
		}
	}
}

func TestSanitizeModelMetaTruncatesLongDescription(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "说"
	}
	got := sanitizeModelMeta("m", model.ModelMeta{Description: "  " + long + "  "})
	if n := len([]rune(got.Description)); n != 200 {
		t.Fatalf("说明应截断到 200 字，实际 %d", n)
	}
}

func TestSanitizeModelMetaKeepsModelName(t *testing.T) {
	got := sanitizeModelMeta("Qwen-Image", model.ModelMeta{Model: "被覆盖"})
	if got.Model != "Qwen-Image" {
		t.Fatalf("模型名应以入参为准，实际 %q", got.Model)
	}
}

func TestNormalizeSizeToken(t *testing.T) {
	ok := map[string]string{
		"1024x1024": "1024x1024",
		"1024*768":  "1024x768",
		" 512X512 ": "512x512",
	}
	bad := []string{"", "abc", "1x1", "1024", "1024x", "20480x1024", "1024x1024x3"}
	for in, want := range ok {
		got, valid := model.NormalizeSizeToken(in)
		if !valid || got != want {
			t.Fatalf("%q 应归一化为 %q，实际 %q（valid=%v）", in, want, got, valid)
		}
	}
	for _, s := range bad {
		if _, valid := model.NormalizeSizeToken(s); valid {
			t.Fatalf("%q 应被拒绝", s)
		}
	}
}
