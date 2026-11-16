package channel

import (
	"strconv"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// knownEndpointSuffixes 上游地址里可能已经写全的端点后缀。
// 顺序从长到短，避免 /completions 抢先匹配 /chat/completions。
var knownEndpointSuffixes = []string{
	"/chat/completions",
	"/completions",
	"/messages",
	"/responses",
	"/embeddings",
}

// baseEndpointSuffix 返回主机地址里已显式写出的端点后缀，没有时返回空串。
//
// 面板不再提供「API 路径」输入框，想让地址更具体时可以把端点直接写进主机地址，
// 例如 https://api.anthropic.com/v1/messages；此时不应再拼接默认路径。
func baseEndpointSuffix(base string) string {
	base = strings.ToLower(strings.TrimRight(strings.TrimSpace(base), "/"))
	for _, s := range knownEndpointSuffixes {
		if strings.HasSuffix(base, s) {
			return s
		}
	}
	return ""
}

// ChatPath 返回渠道对话接口路径，未配置时默认 /chat/completions。
//
// 主机地址已写全端点时返回空串，交给调用方直接使用主机地址。
func ChatPath(ch *model.Channel) string {
	p := ""
	if ch != nil {
		p = strings.TrimSpace(ch.Path)
	}
	if p == "" {
		if ch != nil && baseEndpointSuffix(ch.BaseURL) != "" {
			return ""
		}
		return "/chat/completions"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// ModelsPath 返回渠道「模型列表」接口路径。
//
// 对话端点约定为 <前缀>/chat/completions，模型列表是其同级 <前缀>/models：
// 先去掉结尾的 chat/completions，否则去掉最后一段（兼容 /v1/messages 这类端点）。
// 例：/chat/completions -> /models；/v1/chat/completions -> /v1/models；/v1/messages -> /v1/models。
func ModelsPath(ch *model.Channel) string {
	p := ""
	if ch != nil {
		p = strings.TrimSpace(ch.Path)
	}
	if p == "" {
		return "/models"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimRight(p, "/")

	if strings.HasSuffix(p, "/chat/completions") {
		p = strings.TrimSuffix(p, "/chat/completions")
	} else if i := strings.LastIndex(p, "/"); i > 0 {
		p = p[:i]
	} else {
		p = ""
	}
	return p + "/models"
}

// ModelsURL 返回上游「模型列表」的完整地址，供面板拉取模型使用。
//
// 未填写路径时，若主机地址已写全端点，则按端点前缀推导 /models。
func ModelsURL(ch *model.Channel) string {
	base := BaseURL(ch)
	if ch != nil && strings.TrimSpace(ch.Path) != "" {
		return base + ModelsPath(ch)
	}
	if suf := baseEndpointSuffix(base); suf != "" {
		return strings.TrimRight(base[:len(base)-len(suf)], "/") + "/models"
	}
	return base + "/models"
}

// BaseURL 返回去掉结尾斜杠的上游根地址。
func BaseURL(ch *model.Channel) string {
	if ch == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(ch.BaseURL), "/")
}

// JoinURL 拼接上游地址与路径，并附加原始 query。
func JoinURL(ch *model.Channel, path, rawQuery string) string {
	u := BaseURL(ch) + path
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

func nowMillis() int64 { return time.Now().UnixMilli() }

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
