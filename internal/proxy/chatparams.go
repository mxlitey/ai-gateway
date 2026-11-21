package proxy

import (
	"strings"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// applyChatParams 按渠道声明的对话参数配置改写请求体。
//
// 顺序与生图链路一致：参数改名 -> 参数剥离 -> 参数改写（只补齐客户端没传的字段）。
// 未配置任何项时直接返回原 body，避免每次尝试都复制一份 map。
func applyChatParams(body map[string]any, ch *model.Channel) map[string]any {
	if ch == nil ||
		(len(ch.ParamMap) == 0 && len(ch.DropParams) == 0 && len(ch.StaticParams) == 0) {
		return body
	}
	out := make(map[string]any, len(body)+len(ch.StaticParams))
	for k, v := range body {
		out[k] = v
	}
	for from, to := range ch.ParamMap {
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if from == "" || to == "" || from == to {
			continue
		}
		v, ok := out[from]
		if !ok {
			continue
		}
		out[to] = v
		delete(out, from)
	}
	for _, name := range ch.DropParams {
		if name = strings.TrimSpace(name); name != "" {
			delete(out, name)
		}
	}
	for k, v := range ch.StaticParams {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out
}
