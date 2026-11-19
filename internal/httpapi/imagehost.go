package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// 读取走 imagestore 的静态路由（知道链接即可访问），写入与删除需要有效 API Key；
// 另有一类「签名路径」令牌，由 API Key 签发后可直接上传/删除自己上传的文件，
// 便于把写能力交给 agent 等第三方而不暴露 sk- 密钥（见 uploadtoken.go）。
type ImageHost struct {
	uploads *imagestore.Store
	images  *imagestore.Store
	keys    *store.Store
	baseURL string
	public  bool
	tokens  *uploadTokenIssuer
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
		tokens:  newUploadTokenIssuer(keys),
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
		// 支持「签发签名路径令牌」：客户端可用它上传/删除而不暴露 sk- 密钥
		out["supports_upload_token"] = true
		out["upload_token_endpoint"] = "/v1/images/upload-tokens"
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

// MintUploadToken 处理 POST /v1/images/upload-tokens：签发签名路径令牌。
//
// 需要 sk- 密钥；签出的令牌只具备「上传」与「删除自己上传的文件」两项能力。
func (h *ImageHost) MintUploadToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(r); !ok {
		writeImageError(w, http.StatusUnauthorized, "unauthorized", "缺少或无效的 API Key（Authorization: Bearer sk-...）")
		return
	}
	if h.uploads == nil {
		writeImageError(w, http.StatusNotFound, "not_found", "图床未开启")
		return
	}

	var req struct {
		TTLSeconds int  `json:"ttl_seconds"`
		MaxMB      int  `json:"max_mb"`
	}
	if r.Body != nil {
		// 请求体可选；解析失败时按默认值签发，避免客户端因为多余字段被拒
		_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	}

	ttl := uploadTokenTTLDefault
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	// 单张上限只能收紧，不能放宽到全局上限之上
	maxMB := req.MaxMB
	globalMB := int(h.uploads.MaxBytes() >> 20)
	if maxMB <= 0 || maxMB > globalMB {
		maxMB = globalMB
	}

	token, payload, err := h.tokens.Mint(ttl, maxMB)
	if err != nil {
		writeImageError(w, http.StatusInternalServerError, "internal_error", "签发上传令牌失败: "+err.Error())
		return
	}

	base := h.baseURL
	if base == "" {
		base = proxy.PublicBaseURL(r)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"upload_url": base + h.uploads.Prefix() + token,
		"delete_url": base + h.uploads.Prefix() + token,
		"expires_at": payload.Exp,
		"expires_in": payload.Exp - time.Now().Unix(),
		"max_mb":     payload.MaxMB,
		"max_bytes":  int64(payload.MaxMB) << 20,
		"public":     h.public,
	})
}

// UploadByToken 处理 POST /v1/uploads/{token}：用签名路径上传图片。
func (h *ImageHost) UploadByToken(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeImageError(w, http.StatusNotFound, "not_found", "图床未开启")
		return
	}
	payload, err := h.tokens.Verify(r.PathValue("token"))
	if err != nil {
		h.writeTokenError(w, err)
		return
	}

	limit := h.uploads.MaxBytes()
	if payload.MaxMB > 0 && int64(payload.MaxMB)<<20 < limit {
		limit = int64(payload.MaxMB) << 20
	}

	declared := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if declared != "" && !strings.HasPrefix(declared, "image/") && declared != "application/octet-stream" {
		writeImageError(w, http.StatusUnsupportedMediaType, "unsupported_type", "只接受图片：PNG / JPEG / WebP / GIF")
		return
	}
	if r.ContentLength > limit {
		writeImageError(w, http.StatusRequestEntityTooLarge, "too_large", "图片超过上限 "+formatMB(limit))
		return
	}

	saved, err := h.uploads.SaveReaderAs(r.Body, limit, true, payload.SID)
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

// DeleteByPath 处理 /v1/uploads/ 下的两种 DELETE，并兼容原有的 sk- 单文件删除。
//
//	DELETE /v1/uploads/{token}/{name}  删该令牌上传的单个文件；删空后自动吊销令牌
//	DELETE /v1/uploads/{token}         删该令牌上传的全部文件，成功后吊销令牌
//	DELETE /v1/uploads/{name}          原有入口：sk- 删单个文件（段值不是合法令牌时走这里）
//
// 单删不吊销是有意为之：否则上传多张后删掉一张，其余文件就再也无法通过该令牌清理。
// 但删到最后一张时已经没有可清理的文件，此时自动吊销，避免留下「只能继续上传」的空令牌。
func (h *ImageHost) DeleteByPath(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeImageError(w, http.StatusNotFound, "not_found", "图床未开启")
		return
	}
	raw := r.PathValue("token")
	name := r.PathValue("name")

	// 两段形态且段值不是合法令牌 → 回落为原有的 sk- 单文件删除。
	// （三段形态没有旧行为可回落，令牌无效就是 401。）
	payload, err := h.tokens.Verify(raw)
	if err != nil {
		if name != "" || isTokenShaped(raw) {
			h.writeTokenError(w, err)
			return
		}
		h.Delete(w, r)
		return
	}

	// 不带文件名：整批删除 + 吊销
	if name == "" {
		deleted, delErr := h.uploads.DeleteBySID(payload.SID)
		if delErr != nil {
			// 部分失败时不吊销，否则剩下的文件将无法再清理
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]any{
					"code":    "partial_delete",
					"message": "部分文件删除失败，令牌仍有效，可重试: " + delErr.Error(),
				},
				"deleted": deleted,
			})
			return
		}
		h.tokens.Revoke(payload.SID, payload.Exp)
		writeJSON(w, http.StatusOK, map[string]any{
			"deleted":    deleted,
			"revoked":    true,
			"expires_at": payload.Exp,
		})
		return
	}

	if !imagestore.BelongsTo(name, payload.SID) {
		// 路径合法但文件不属于该令牌：明确拒绝，避免被当成「删别人的图」
		writeImageError(w, http.StatusForbidden, "forbidden",
			"该令牌只能删除自己上传的文件")
		return
	}
	if delErr := h.uploads.Delete(name); delErr != nil {
		if os.IsNotExist(delErr) {
			writeImageError(w, http.StatusNotFound, "not_found", "图片不存在")
			return
		}
		writeImageError(w, http.StatusInternalServerError, "internal_error", "删除图片失败: "+delErr.Error())
		return
	}

	// 删空了就吊销：逐张删到最后一张，效果与整批删除一致，令牌不会留下
	// 「只能继续上传」的半残状态。列表读失败时不吊销，宁可多留一个令牌。
	revoked := false
	if rest, listErr := h.uploads.ListBySID(payload.SID); listErr == nil && len(rest) == 0 {
		h.tokens.Revoke(payload.SID, payload.Exp)
		revoked = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": 1,
		"revoked": revoked,
	})
}

// isTokenShaped 判断路径段是否长得像令牌（两段 base64url + 签名）。
// 用于区分「用户传了坏令牌」和「用户在删文件名」，后者应回落到 sk- 鉴权。
func isTokenShaped(seg string) bool {
	if seg == "" || len(seg) < 24 {
		return false
	}
	dot := strings.IndexByte(seg, '.')
	return dot > 8 && dot < len(seg)-16
}

// writeTokenError 把令牌校验错误映射成对外响应。
func (h *ImageHost) writeTokenError(w http.ResponseWriter, err error) {
	code := "invalid_token"
	msg := "上传令牌无效"
	switch {
	case errors.Is(err, errUploadTokenExpired):
		code, msg = "token_expired", "上传令牌已过期，请重新签发"
	case errors.Is(err, errUploadTokenRevoked):
		code, msg = "token_revoked", "上传令牌已被吊销"
	}
	writeImageError(w, http.StatusUnauthorized, code, msg)
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
