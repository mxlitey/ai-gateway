package model

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SanitizeModelMeta 规整表单输入：去空白、过滤非法值、给数值加上合理上界。
//
// 元数据只是「对外声明」，不做业务校验，因此处理从宽；但脏数据会原样传播到
// /v1/models 的响应里，所以明显无意义的值（负数、超长文本、乱写的尺寸）在这里拦掉。
//
// 放在 model 包而不是 admin 包，是因为管理 MCP 也要走同一套规整，两处不能各写一份。
func SanitizeModelMeta(name string, in ModelMeta) ModelMeta {
	out := in
	out.Model = name
	out.Description = strings.TrimSpace(out.Description)
	if utf8.RuneCountInString(out.Description) > 200 {
		out.Description = string([]rune(out.Description)[:200])
	}
	out.MaxRefCount = ClampInt(out.MaxRefCount, 0, 32)
	out.MaxRefSidePx = ClampInt(out.MaxRefSidePx, 0, 16384)
	out.MaxRefMB = ClampInt(out.MaxRefMB, 0, 1024)
	out.MaxN = ClampInt(out.MaxN, 0, 16)
	out.MaxNRef = ClampInt(out.MaxNRef, 0, 16)
	out.DefaultN = ClampInt(out.DefaultN, 0, 16)

	// 格式只保留受支持的三类，统一小写
	formats := make([]string, 0, len(in.RefFormats))
	seenFmt := map[string]bool{}
	for _, f := range in.RefFormats {
		f = strings.ToLower(strings.TrimSpace(f))
		switch f {
		case "png", "jpeg", "webp":
			if !seenFmt[f] {
				seenFmt[f] = true
				formats = append(formats, f)
			}
		}
	}
	out.RefFormats = formats

	// 尺寸只保留「数字x数字」形态，统一成小写 x 并去重
	sizes := make([]string, 0, len(in.Sizes))
	seenSize := map[string]bool{}
	for _, s := range in.Sizes {
		norm, ok := NormalizeSizeToken(s)
		if !ok || seenSize[norm] {
			continue
		}
		seenSize[norm] = true
		sizes = append(sizes, norm)
	}
	out.Sizes = sizes
	return out
}

// NormalizeSizeToken 把 "1024x1024" / "1024*1024" / "1024X1024" 统一成小写 x 形态。
// 宽高各自限制在 64..16384：太小没有意义，太大超出任何上游的接受范围。
func NormalizeSizeToken(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == 'x' || r == '*' })
	if len(parts) != 2 {
		return "", false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 64 || n > 16384 {
			return "", false
		}
	}
	return parts[0] + "x" + parts[1], true
}

// ClampInt 把 v 夹到 [lo, hi]。
func ClampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ValidatePrice 校验一条价格表单：单位合法、单价非负、按 token 计费必须有兜底档、
// 峰谷时段的起止与星期合法。
//
// 与 SanitizeModelMeta 同理放在 model 包，供面板与管理 MCP 共用。
func ValidatePrice(unit string, tiers []PriceTier, cachePrice, requestPrice float64, windows []TimeWindow) error {
	if unit != "" && unit != PriceUnitToken && unit != PriceUnitRequest {
		return fmt.Errorf("unit 只能是 per_token 或 per_request")
	}
	if cachePrice < 0 || requestPrice < 0 {
		return fmt.Errorf("价格不能为负数")
	}
	if unit == PriceUnitRequest {
		return nil
	}
	if len(tiers) == 0 {
		return fmt.Errorf("按 token 计费至少要有一档价格")
	}
	for i, t := range tiers {
		if t.PromptPrice < 0 || t.CompletionPrice < 0 {
			return fmt.Errorf("第 %d 档价格不能为负数", i+1)
		}
		if t.UpTo < 0 {
			return fmt.Errorf("第 %d 档的输入上限不能为负数", i+1)
		}
		if t.UpTo == 0 && i != len(tiers)-1 {
			return fmt.Errorf("只有最后一档可以不限输入长度")
		}
	}
	if last := tiers[len(tiers)-1]; last.UpTo != 0 {
		return fmt.Errorf("最后一档需要不限输入长度（上限留空）")
	}
	for i, w := range windows {
		if !validClock(w.Start) || !validClock(w.End) {
			return fmt.Errorf("第 %d 条时段的起止时间需为 HH:MM", i+1)
		}
		if w.Start == w.End {
			return fmt.Errorf("第 %d 条时段的起止时间不能相同", i+1)
		}
		if w.Factor <= 0 {
			return fmt.Errorf("第 %d 条时段的系数需大于 0", i+1)
		}
		for _, d := range w.Days {
			if d < 0 || d > 6 {
				return fmt.Errorf("第 %d 条时段的星期需为 0-6（0=周日）", i+1)
			}
		}
	}
	return nil
}

// validClock 判断 HH:MM，小时 0-24、分钟 0-59；24:00 只允许作为结束时间使用，这里一并放行。
// 与面板原有的解析口径一致：允许 "9:00" 这种一位小时。
func validClock(s string) bool {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return false
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return false
	}
	m, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return false
	}
	if h < 0 || h > 24 || m < 0 || m > 59 {
		return false
	}
	return !(h == 24 && m != 0)
}
