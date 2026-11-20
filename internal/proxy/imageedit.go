package proxy

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// imageEditMaxBytes 编辑请求 multipart 的总大小上限。
// 参考图本身按 referenceMaxBytes 逐张限制，这里只兜住恶意超大请求。
const imageEditMaxBytes = 32 << 20

// 参考图限制：与上游 base64 限制对齐（单张 3 MB、合计 7 M 字符）。
const (
	referenceMaxBytes = 3 << 20
	referenceMaxChars = 7 << 20
)

// 参考图相关的错误：调用方按错误类型映射状态码。
var (
	errInvalidReference  = errors.New("参考图不合法")
	errReferenceTooLarge = errors.New("参考图过大")
)

// imageRefErrorStatus 把参考图错误映射成 HTTP 状态码与稳定的错误码。
func imageRefErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, errReferenceTooLarge):
		return http.StatusRequestEntityTooLarge, "reference_exceeds_base64_limit"
	case errors.Is(err, errInvalidReference):
		return http.StatusBadRequest, "invalid_reference"
	}
	return http.StatusBadRequest, "invalid_reference"
}

// jsonErrorCode 写出带稳定错误码的错误响应（OpenAI 兼容 + code 字段）。
func jsonErrorCode(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}

// editScalarFields 从 multipart 里透传给生图流程的标量字段（OpenAI 标准 edits 参数）。
// mask 暂不支持：渠道未配置遮罩能力，忽略即可。
var editScalarFields = []string{
	"model", "prompt", "size", "response_format", "quality", "background",
	"output_format", "output_compression", "user",
}

// HandleImageEdit 处理 POST /v1/images/edits：标准 multipart 参考图上传。
//
// 网关把 multipart 的 image 文件转成 data URI，交给与 /v1/images/generations
// 完全相同的 channel 改写链路，因此对客户端保持标准 OpenAI 协议。
func (h *Handler) HandleImageEdit(w http.ResponseWriter, r *http.Request) {
	apiKey, errMsg := h.VerifyAPIKey(r)
	if apiKey == nil {
		h.logGatewayError("", http.StatusUnauthorized, "鉴权失败: "+errMsg)
		jsonError(w, http.StatusUnauthorized, errMsg, "invalid_request_error")
		return
	}
	if apiKey.ChannelScope == model.ScopeNone ||
		(apiKey.ChannelScope == model.ScopeSelected && len(apiKey.ChannelIDs) == 0) {
		h.logGatewayError("", http.StatusForbidden, "该密钥未获得渠道访问权限")
		jsonError(w, http.StatusForbidden, "该密钥未获得渠道访问权限", "invalid_request_error")
		return
	}

	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, imageEditMaxBytes)
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		msg := "解析 multipart 请求失败: " + err.Error()
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			msg = fmt.Sprintf("请求体过大（超过 %d MB）", imageEditMaxBytes>>20)
			h.logGatewayError("", http.StatusRequestEntityTooLarge, "生图编辑请求被拒绝: "+msg)
			jsonErrorCode(w, http.StatusRequestEntityTooLarge, msg, "request_too_large")
			return
		}
		h.logGatewayError("", http.StatusBadRequest, "生图编辑请求被拒绝: "+msg)
		jsonError(w, http.StatusBadRequest, msg, "invalid_request_error")
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}

	refs, err := editReferenceImages(r)
	if err != nil {
		status, code := imageRefErrorStatus(err)
		msg := err.Error()
		h.logGatewayError("", status, "生图编辑请求被拒绝: "+msg)
		jsonErrorCode(w, status, msg, code)
		return
	}

	body := map[string]any{}
	for _, name := range editScalarFields {
		v := strings.TrimSpace(r.FormValue(name))
		if v == "" {
			continue
		}
		if name == "output_compression" {
			if n, convErr := strconv.Atoi(v); convErr == nil {
				body[name] = n
			}
			continue
		}
		body[name] = v
	}
	if v := strings.TrimSpace(r.FormValue("n")); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 {
			body["n"] = n
		}
	}
	body["image_url"] = refs

	modelName, _ := body["model"].(string)
	// 编辑请求的参考图以 base64 内联，不适合落进作业表；异步作业只走 generations。
	if h.isImageJobRequest(r, body) {
		msg := "图片编辑接口不支持异步作业，请改用 POST /v1/images/generations 并传入 async"
		h.logGatewayError(modelName, http.StatusBadRequest, "生图编辑请求被拒绝: "+msg)
		jsonError(w, http.StatusBadRequest, msg, "invalid_request_error")
		return
	}

	rec := newRecorder(h.store, h.pricer, apiKey, "images", false, modelName)
	defer rec.Commit()
	h.handleImages(w, r, body, apiKey.ChannelIDs, apiKey, rec)
}

// editReferenceImages 读取 multipart 中的 image / image[] 文件，转成 data URI 列表。
// 只接受 PNG/JPEG/WebP，按文件头嗅探类型，不信任客户端声明的 Content-Type。
func editReferenceImages(r *http.Request) ([]string, error) {
	if r.MultipartForm == nil {
		return nil, fmt.Errorf("%w：缺少 multipart 表单", errInvalidReference)
	}
	files := r.MultipartForm.File["image"]
	files = append(files, r.MultipartForm.File["image[]"]...)
	if len(files) == 0 {
		return nil, fmt.Errorf("%w：缺少 image 文件", errInvalidReference)
	}

	out := make([]string, 0, len(files))
	total := 0
	for _, fh := range files {
		if fh.Size > referenceMaxBytes {
			return nil, fmt.Errorf("%w：单张 %d KB，上限 %d MB；请压缩后重试或改用公网图片地址",
				errReferenceTooLarge, fh.Size>>10, referenceMaxBytes>>20)
		}
		f, err := fh.Open()
		if err != nil {
			return nil, fmt.Errorf("%w：读取上传文件失败: %v", errInvalidReference, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(f, referenceMaxBytes+1))
		_ = f.Close()
		if readErr != nil {
			return nil, fmt.Errorf("%w：读取上传文件失败: %v", errInvalidReference, readErr)
		}
		if len(data) > referenceMaxBytes {
			return nil, fmt.Errorf("%w：单张超过 %d MB；请压缩后重试或改用公网图片地址",
				errReferenceTooLarge, referenceMaxBytes>>20)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("%w：image 文件内容为空", errInvalidReference)
		}

		contentType := http.DetectContentType(data)
		switch contentType {
		case "image/png", "image/jpeg", "image/webp":
		default:
			return nil, fmt.Errorf("%w：不支持的类型 %s，仅接受 PNG/JPEG/WebP",
				errInvalidReference, contentType)
		}

		encoded := "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
		total += len(encoded)
		if total > referenceMaxChars {
			return nil, fmt.Errorf("%w：参考图合计超过 %d M 字符；请减少参考图数量或压缩后重试",
				errReferenceTooLarge, referenceMaxChars>>20)
		}
		out = append(out, encoded)
	}
	return out, nil
}
