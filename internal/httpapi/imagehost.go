package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/imagestore"
	"github.com/czg95/ai-gateway-v2/internal/proxy"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// ImageHost 内置图床：上传与删除接口。
//
// 读取走 imagestore 的静态路由（知道链接即可访问），写入与删除需要有效 API Key。
type ImageHost struct {
	uploads *imagestore.Store
	images  *imagestore.Store
	keys    *store.Store
	baseURL string
	public  bool
}

// NewImageHost 创建图床 handler。baseURL 为空时按请求 Host 推导。
// public 表示对外声明的公网可达性，供客户端决定「传链接」还是「传 base64」。
func NewImageHost(uploads, images *imagestore.Store, keys *store.Store, baseURL string, public bool) *ImageHost {
	return &ImageHost{
		uploads: uploads,
		images:  images,
		keys:    keys,
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		public:  public,
	}
}

// Capabilities 处理 GET /v1/images/host：声明图床是否开启、是否公网可达。
//
// 这是「声明」而不是探测：网关无法证明 NAT/防火墙真的放行。
func (h *ImageHost) Capabilities(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(r); !ok {
		writeImageError(w, http.StatusUnauthorized, "unauthorized", "缺少或无效的 API Key（Authorization: Bearer sk-...）")
		return
	}
	out := map[string]any{
		"enabled": h.uploads != nil,
		"public":  h.public,
	}
	if h.baseURL != "" {
		out["public_base"] = h.baseURL
	}
	if h.uploads != nil {
		out["url_prefix"] = h.uploads.Prefix()
		out["max_mb"] = h.uploads.MaxBytes() >> 20
		out["ttl_seconds"] = int(h.uploads.Retention().Seconds())
	}
	writeJSON(w, http.StatusOK, out)
}

// Upload 处理 POST /v1/images/uploads。
//
// 请求体为图片原始字节；按文件头判断类型，只接受 PNG/JPEG/WebP/GIF。
func (h *ImageHost) Upload(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(r); !ok {
		writeImageError(w, http.StatusUnauthorized, "unauthorized", "缺少或无效的 API Key（Authorization: Bearer sk-...）")
		return
	}
	if h.uploads == nil {
		writeImageError(w, http.StatusNotFound, "not_found", "图床未开启")
		return
	}

	declared := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if declared != "" && !strings.HasPrefix(declared, "image/") && declared != "application/octet-stream" {
		writeImageError(w, http.StatusUnsupportedMediaType, "unsupported_type", "只接受图片：PNG / JPEG / WebP / GIF")
		return
	}
	if r.ContentLength > h.uploads.MaxBytes() {
		writeImageError(w, http.StatusRequestEntityTooLarge, "too_large",
			"图片超过上限 "+formatMB(h.uploads.MaxBytes()))
		return
	}

	saved, err := h.uploads.SaveReader(r.Body, h.uploads.MaxBytes(), true)
	switch {
	case errors.Is(err, imagestore.ErrUnsupportedType):
		writeImageError(w, http.StatusUnsupportedMediaType, "unsupported_type", "不是有效的图片：只接受 PNG / JPEG / WebP / GIF")
		return
	case errors.Is(err, imagestore.ErrTooLarge):
		writeImageError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
		return
	case errors.Is(err, imagestore.ErrEmptyImage):
		writeImageError(w, http.StatusBadRequest, "invalid_request", "请求体为空")
		return
	case err != nil:
		writeImageError(w, http.StatusInternalServerError, "internal_error", "保存图片失败: "+err.Error())
		return
	}

	base := h.baseURL
	if base == "" {
		base = proxy.PublicBaseURL(r)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           saved.Name,
		"url":          base + h.uploads.URL(saved.Name),
		"bytes":        saved.Bytes,
		"content_type": saved.ContentType,
		"expires_at":   time.Now().Add(h.uploads.Retention()).Unix(),
		"public":       h.public,
	})
}

// Delete 处理 DELETE /v1/images/files/{name} 与 DELETE /v1/uploads/{name}。
func (h *ImageHost) Delete(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(r); !ok {
		writeImageError(w, http.StatusUnauthorized, "unauthorized", "缺少或无效的 API Key（Authorization: Bearer sk-...）")
		return
	}

	var target *imagestore.Store
	var name string
	switch {
	case strings.HasPrefix(r.URL.Path, imagestore.UploadURLPrefix):
		target = h.uploads
		name = strings.TrimPrefix(r.URL.Path, imagestore.UploadURLPrefix)
	case strings.HasPrefix(r.URL.Path, imagestore.URLPrefix):
		target = h.images
		name = strings.TrimPrefix(r.URL.Path, imagestore.URLPrefix)
	}
	if target == nil {
		writeImageError(w, http.StatusNotFound, "not_found", "图片不存在")
		return
	}
	if err := target.Delete(name); err != nil {
		if os.IsNotExist(err) {
			writeImageError(w, http.StatusNotFound, "not_found", "图片不存在")
			return
		}
		writeImageError(w, http.StatusInternalServerError, "internal_error", "删除图片失败: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ImageHost) authorize(r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(key, "Bearer ") {
		key = strings.TrimSpace(strings.TrimPrefix(key, "Bearer "))
	} else {
		key = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	if key == "" || h.keys == nil {
		return "", false
	}
	k, err := h.keys.FindEnabledAPIKeyByValue(key)
	if err != nil || k == nil {
		return "", false
	}
	return key, true
}

func writeImageError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func formatMB(n int64) string {
	return fmt.Sprintf("%d MB", n>>20)
}
