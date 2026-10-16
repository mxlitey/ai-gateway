// Package app 把网关的装配与生命周期封装成可反复启停的实例，
// 供命令行 serve（含 Windows 服务）与桌面窗口共用。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/admin"
	"github.com/czg95/ai-gateway-v2/internal/config"
	"github.com/czg95/ai-gateway-v2/internal/httpapi"
	"github.com/czg95/ai-gateway-v2/internal/plugin"
	"github.com/czg95/ai-gateway-v2/internal/pricing"
	"github.com/czg95/ai-gateway-v2/internal/proxy"
	"github.com/czg95/ai-gateway-v2/internal/store"
	"github.com/czg95/ai-gateway-v2/internal/version"
)

// App 一个可启停的网关实例。
type App struct {
	cfg *config.Config

	mu      sync.Mutex
	running bool
	errCh   chan error
	cancel  context.CancelFunc

	store *store.Store
	host  *plugin.Host
	srv   *http.Server
}

// New 创建实例（此时尚未占用端口与数据目录）。
func New(cfg *config.Config) *App {
	return &App{cfg: cfg, errCh: make(chan error, 1)}
}

// Config 返回运行配置。
func (a *App) Config() *config.Config { return a.cfg }

// Addr 返回监听地址。
func (a *App) Addr() string {
	return fmt.Sprintf("%s:%d", a.cfg.Host, a.cfg.Port)
}

// URL 返回管理面板地址。
func (a *App) URL() string {
	host := a.cfg.Host
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, a.cfg.Port)
}

// Running 返回是否正在运行。
func (a *App) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// Host 返回插件宿主；未启动时为 nil。
func (a *App) Host() *plugin.Host {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.host
}

// Store 返回当前数据库句柄；未启动时为 nil。
func (a *App) Store() *store.Store {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.store
}

// Err 返回服务异常退出通道。
func (a *App) Err() <-chan error { return a.errCh }

// Start 启动网关：占用端口、打开数据目录、拉起插件宿主与后台任务。
func (a *App) Start() error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return fmt.Errorf("网关已在运行")
	}
	a.errCh = make(chan error, 1)
	a.mu.Unlock()

	if err := os.MkdirAll(a.cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}

	addr := a.Addr()
	// 先占用端口：端口被占时直接报错，避免「启动了但没在监听」
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", addr, err)
	}

	st, err := store.Open(a.cfg.DBPath())
	if err != nil {
		_ = ln.Close()
		return err
	}

	host := plugin.New(a.cfg, st, slog.Default())

	pricer := pricing.New(st)
	if err := pricer.Reload(); err != nil {
		slog.Error("[pricing] 载入价格表失败", "err", err)
	}

	api := admin.NewAPI(st, host, pricer, a.cfg)
	if a.cfg.AdminPassword != "" {
		if initialized, err := api.Service().IsInitialized(); err == nil && !initialized {
			if err := api.Service().Setup(a.cfg.AdminPassword); err != nil {
				slog.Error("初始化管理员密码失败", "err", err)
			} else {
				slog.Info("已通过 --admin-password 完成首次初始化")
			}
		}
	}

	router := httpapi.NewRouter(httpapi.Deps{Proxy: proxy.New(st, pricer), Admin: api})
	srv := &http.Server{Addr: addr, Handler: router, ReadHeaderTimeout: 15 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())

	a.mu.Lock()
	a.store, a.host, a.srv, a.cancel, a.running = st, host, srv, cancel, true
	a.mu.Unlock()

	go func() {
		slog.Info("AI Gateway 已启动",
			"url", a.URL(),
			"data_dir", a.cfg.DataDir,
			"plugins_dir", a.cfg.PluginsDir,
			"version", version.Version,
		)
		if a.cfg.Host != "127.0.0.1" && a.cfg.Host != "localhost" {
			slog.Warn("正在监听非回环地址，请确认网络暴露风险", "host", a.cfg.Host)
		}
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case a.errCh <- fmt.Errorf("HTTP 服务异常退出: %w", err):
			default:
			}
		}
	}()

	// 插件宿主：扫描目录并按开关拉起插件（开发模式下不自动启动）
	if !a.cfg.Dev {
		go func() {
			if err := host.Discover(); err != nil {
				slog.Error("插件扫描失败", "err", err)
			}
		}()
	}

	// 自动签到：宿主定期对运行中、声明 checkin 能力的插件补签（插件不再自行定时签到）
	host.StartAutoCheckin(ctx)

	// 价格快照：启动后立刻尝试一次，之后每小时复查（同一小时内只拉一次，失败沿用现有价格表）
	go func() {
		client := &http.Client{Timeout: 45 * time.Second}
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			pricer.RefreshIfDue(ctx, client, pricing.Endpoint(), time.Hour)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	return nil
}

// Stop 停止网关：关闭 HTTP 服务、回收插件进程、释放数据库。
func (a *App) Stop() error {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return nil
	}
	srv, host, st, cancel := a.srv, a.host, a.store, a.cancel
	a.store, a.host, a.srv, a.cancel, a.running = nil, nil, nil, nil, false
	a.mu.Unlock()

	cancel()

	// 优雅关闭只给很短的窗口：流式请求会长时间占用连接，
	// 若等满超时会让「退出网关」明显卡顿；超时后直接强制关闭剩余连接。
	ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
	defer done()
	err := srv.Shutdown(ctx)
	if err != nil {
		_ = srv.Close()
	}

	if host != nil {
		host.Shutdown()
	}
	if st != nil {
		_ = st.Close()
	}
	return err
}