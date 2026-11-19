package admin

import (
	"testing"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

func TestSanitizeModelMetaClampsNumbers(t *testing.T) {
	got := sanitizeModelMeta("m", model.ModelMeta{
		MaxRefCount: -5,
		MaxN:        999,
		MaxNRef:     -8,
		MinSidePx:   99999,
		MaxSidePx:   -1,
	})
	if got.MaxRefCount != 0 {
		t.Fatalf("负数应归零，实际 %d", got.MaxRefCount)
	}
	if got.MaxN != 16 {
		t.Fatalf("超上限应截断到 16，实际 %d", got.MaxN)
	}
	if got.MaxNRef != 0 {
		t.Fatalf("负数应归零，实际 %d", got.MaxNRef)
	}
	if got.MinSidePx != 16384 {
		t.Fatalf("超上限应截断到 16384，实际 %d", got.MinSidePx)
	}
	if got.MaxSidePx != 0 {
		t.Fatalf("负数应归零，实际 %d", got.MaxSidePx)
	}
}

// TestSanitizeModelMetaFixesInvertedRange 下限大于上限时以上限为准。
func TestSanitizeModelMetaFixesInvertedRange(t *testing.T) {
	got := sanitizeModelMeta("m", model.ModelMeta{MinSidePx: 4096, MaxSidePx: 1024})
	if got.MinSidePx != 1024 || got.MaxSidePx != 1024 {
		t.Fatalf("下限应回落到上限，实际 min=%d max=%d", got.MinSidePx, got.MaxSidePx)
	}
}

func TestSanitizeModelMetaFiltersFormats(t *testing.T) {
	got := sanitizeModelMeta("m", model.ModelMeta{
		OutputFormats: []string{"PNG", " gif ", "webp", "png", "jpeg"},
	})
	wantFormats := []string{"png", "webp", "jpeg"}
	if len(got.OutputFormats) != len(wantFormats) {
		t.Fatalf("格式过滤后应剩 %d 项，实际 %v", len(wantFormats), got.OutputFormats)
	}
	for i, f := range wantFormats {
		if got.OutputFormats[i] != f {
			t.Fatalf("第 %d 项应为 %s，实际 %s", i, f, got.OutputFormats[i])
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
