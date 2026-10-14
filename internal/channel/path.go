package channel

import (
	"strconv"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// ChatPath 返回渠道对话接口路径，未配置时默认 /chat/completions。
func ChatPath(ch *model.Channel) string {
	p := ""
	if ch != nil {
		p = strings.TrimSpace(ch.Path)
	}
	if p == "" {
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