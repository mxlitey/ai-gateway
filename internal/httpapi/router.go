// Package httpapi 组装 HTTP 路由、CORS 与内嵌前端资源。
package httpapi

import (
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/czg95/ai-gateway-v2/internal/admin"
	"github.com/czg95/ai-gateway-v2/internal/proxy"
	"github.com/czg95/ai-gateway-v2/web"
)

// Deps 路由依赖。
type Deps struct {
	Proxy *proxy.Handler
	Admin *admin.API
}

// NewRouter 组装完整路由。
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("AI Gateway is running."))
	})

	// /v1/* 代理
	mux.HandleFunc("/v1/", d.Proxy.Handle)
	mux.HandleFunc("GET /v1", d.Proxy.Handle)

	// /admin/api/* 管理接口（鉴权在 handler 内部根据路径区分）
	mux.HandleFunc("/admin/api/", d.Admin.Handle)
	mux.HandleFunc("/admin/api", d.Admin.Handle)

	// 其余走前端静态资源（含 SPA 回退）
	mux.Handle("/", staticHandler())

	return withCORS(mux)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version")
		h.Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func staticHandler() http.Handler {
	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "前端资源不可用: "+err.Error(), http.StatusInternalServerError)
		})
	}
	fileServer := http.FileServerFS(sub)

	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		f, err := sub.Open("index.html")
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("前端尚未构建。请先在 web/ 目录执行前端构建，再重新编译本程序。"))
			return
		}
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "读取前端资源失败", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			serveIndex(w, r)
			return
		}
		if f, err := sub.Open(p); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA 回退：未知路径交给前端路由
		serveIndex(w, r)
	})
}