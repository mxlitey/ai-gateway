package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/storage"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// Storage 对象存储凭证签发接口。
//
// 网关不中转任何文件字节：客户端拿到预签名 URL 后直传/直读对象存储。
// 未配置对象存储时（store 为 nil）所有接口返回 501。
// 面板保存对象存储设置后通过 Set 热替换句柄，无需重启。
type Storage struct {
	store atomic.Pointer[storage.Store]
	keys  *store.Store
}

// NewStorage 创建对象存储接口。s 为 nil 表示未配置。
func NewStorage(s *storage.Store, keys *store.Store) *Storage {
	h := &Storage{keys: keys}
	h.Set(s)
	return h
}

// Set 替换当前对象存储句柄；nil 表示未配置（接口随即返回 501）。
func (h *Storage) Set(s *storage.Store) {
	if s == nil {
		h.store.Store(nil)
		return
	}
	h.store.Store(s)
}

// Enabled 报告当前是否已配置对象存储。
func (h *Storage) Enabled() bool { return h.store.Load() != nil }

// ticketRequest 签发直传凭证的请求体。整个请求体可选。
type ticketRequest struct {
	// Key 指定对象键；为空时由网关生成。
	Key string `json:"key"`
	// Filename 仅用于从扩展名推断对象键后缀。
	Filename string `json:"filename"`
	// TTLSeconds 有效期（秒）；超过服务端上限时按上限截断。
	TTLSeconds int `json:"ttl_seconds"`
}

// Ticket 处理 POST /v1/storage/tickets：签发上传/下载预签名 URL。
func (h *Storage) Ticket(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(r); !ok {
		writeJSON(w, http.StatusUnauthorized, errorBody("unauthorized",
			"缺少或无效的 API Key（Authorization: Bearer sk-...）"))
		return
	}
	st := h.store.Load()
	if st == nil {
		// 未配置对象存储：明确回 501，而不是让客户端误以为是参数问题
		writeJSON(w, http.StatusNotImplemented, errorBody("storage_not_configured",
			"网关未配置对象存储，请先登录管理面板在「设置 → 对象存储」中填写并保存"))
		return
	}

	var req ticketRequest
	if r.Body != nil {
		// 请求体可选；解析失败时按默认值签发，避免客户端因多余字段被拒
		_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req)
	}

	key := strings.TrimPrefix(strings.TrimSpace(req.Key), "/")
	if key == "" {
		key = st.NewUploadKey(extOf(req.Filename))
	} else if strings.HasSuffix(key, "/") {
		key += st.NewUploadKey(extOf(req.Filename))
	}

	ttl := st.TTL()
	// 客户端只能收紧有效期，不能突破服务端配置的上限
	if req.TTLSeconds > 0 {
		if requested := time.Duration(req.TTLSeconds) * time.Second; requested < ttl {
			ttl = requested
		}
	}
	ticket, err := st.Ticket(key, ttl)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal_error", "签发预签名 URL 失败: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, ticket)
}

// authorize 校验 API Key；对象存储凭证等同于写能力，必须持有效密钥。
func (h *Storage) authorize(r *http.Request) (string, bool) {
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

// extOf 从文件名推断扩展名（含点号，不含则返回空）。
func extOf(name string) string {
	name = strings.TrimSpace(name)
	i := strings.LastIndexByte(name, '.')
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return name[i+1:]
}

func errorBody(code, message string) map[string]any {
	return map[string]any{
		"error": map[string]any{"code": code, "message": message},
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
