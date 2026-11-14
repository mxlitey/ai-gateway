package proxy

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/czg95/ai-gateway-v2/internal/imagestore"
)

// 上游对参考图 base64 的常见限制：单张 3 MB 原始字节、合计 7 M 字符。
// 与生图 MCP 的默认预算保持一致，避免两边判定不同。
const (
	referenceMaxBytes = 3 << 20
	referenceMaxChars = 7 << 20
)

var (
	// errReferenceTooLarge 参考图超过上游 base64 上限，客户端需自行解决。
	errReferenceTooLarge = errors.New("参考图超过上游 base64 上限")
	// errUploadedRefMissing 客户端引用的图床文件已过期或不存在。
	errUploadedRefMissing = errors.New("参考图已过期或不存在")
)

// rewriteImageRefs 在网关非公网可达时，把指向自家图床的参考图链接转成 base64。
//
// 只处理 image_url 中以 /v1/uploads/ 开头的地址；公网 URL、data URI 一律不动。
// 公网可达时上游能自己下载，保持原样即可。
func (h *Handler) rewriteImageRefs(body map[string]any) error {
	if h.uploads == nil || h.imageHostPublic {
		return nil
	}
	raw, ok := body["image_url"]
	if !ok {
		return nil
	}

	total := 0
	switch v := raw.(type) {
	case string:
		s, err := h.convertUploadRef(v, &total)
		if err != nil {
			return err
		}
		body["image_url"] = s
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			c, err := h.convertUploadRef(s, &total)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		body["image_url"] = out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				out = append(out, item)
				continue
			}
			c, err := h.convertUploadRef(s, &total)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		body["image_url"] = out
	}
	return nil
}

// convertUploadRef 把单个图床链接替换成 data URI；其他形式原样返回。
func (h *Handler) convertUploadRef(raw string, total *int) (string, error) {
	s := strings.TrimSpace(raw)
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return raw, nil
	}
	if !strings.HasPrefix(u.Path, imagestore.UploadURLPrefix) {
		return raw, nil
	}
	name := strings.TrimPrefix(u.Path, imagestore.UploadURLPrefix)
	if name == "" || strings.Contains(name, "/") {
		return raw, nil
	}

	data, ct, err := h.uploads.Load(name)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", errUploadedRefMissing, name)
		}
		return "", fmt.Errorf("读取图床文件失败: %w", err)
	}
	if len(data) > referenceMaxBytes {
		return "", fmt.Errorf("%w：单张 %d KB，上限 %d MB；请压缩后重试或改用公网图片地址",
			errReferenceTooLarge, len(data)>>10, referenceMaxBytes>>20)
	}

	encoded := "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data)
	*total += len(encoded)
	if *total > referenceMaxChars {
		return "", fmt.Errorf("%w：参考图合计超过 %d M 字符；请减少参考图数量或改用公网图片地址",
			errReferenceTooLarge, referenceMaxChars>>20)
	}
	return encoded, nil
}

// imageRefErrorStatus 把参考图相关的错误映射为 HTTP 状态与稳定错误码。
func imageRefErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, errReferenceTooLarge):
		return http.StatusRequestEntityTooLarge, "reference_exceeds_base64_limit"
	case errors.Is(err, errUploadedRefMissing):
		return http.StatusBadRequest, "reference_expired"
	default:
		return http.StatusBadRequest, "invalid_reference"
	}
}

func jsonErrorCode(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}
