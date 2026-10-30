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
	"github.com/czg95/ai-gateway-v2/internal/adminmcp"
	"github.com/czg95/ai-gateway-v2/internal/config"
	"github.com/czg95/ai-gateway-v2/internal/httpapi"
	"github.com/czg95/ai-gateway-v2/internal/mcp"
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

	store  *store.Store
	host   *plugin.Host
	mcpSvc *mcp.Service
	srv    *http.Server
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

	// 运行期设置：日志级别、插件监管策略、价格刷新节奏（面板可改，这里取启动时的值）
	runtime, err := st.LoadRuntimeSettings()
	if err != nil {
		slog.Error("读取运行期设置失败，改用默认值", "err", err)
		runtime = store.DefaultRuntimeSettings()
	}
	// 日志级别：面板设置优先，其次启动参数 / 环境变量
	if runtime.LogLevel != "" {
		config.ApplyLogLevel(runtime.LogLevel)
	} else {
		config.ApplyLogLevel(a.cfg.LogLevel)
	}

	host := plugin.New(a.cfg, st, slog.Default())
	host.SetPolicy(plugin.Policy{
		ProbeInterval:       time.Duration(runtime.PluginProbeIntervalSeconds) * time.Second,
		ProbeTimeout:        time.Duration(runtime.PluginProbeTimeoutSeconds) * time.Second,
		UnhealthyThreshold:  runtime.PluginUnhealthyThreshold,
		MaxFailures:         runtime.PluginMaxFailures,
		AutoCheckinInterval: time.Duration(runtime.PluginAutoCheckinMinutes) * time.Minute,
	})

	pricer := pricing.New(st)
	if err := pricer.Reload(); err != nil {
		slog.Error("[pricing] 载入价格表失败", "err", err)
	}

	api := admin.NewAPI(st, host, pricer, a.cfg)
	proxyHandler := proxy.New(st, pricer)
	// 管理端可查询/重置上游密钥冷却状态
	api.SetCooler(proxyHandler.Cooler())

	// MCP 聚合：对外一个 MCP 服务器，内聚多台 Streamable HTTP 上游
	mcpSvc := mcp.NewService(st, a.cfg)
	// 本地 MCP：扫描 mcp 目录并拉起 Python 进程（异步启动，不阻塞网关就绪）
	mcpSvc.StartLocal()
	mcpSvc.Warmup()
	mcpHandler := mcp.NewHandler(st, mcpSvc)
	api.SetMCP(mcpSvc)

	// 管理 MCP（/admin/mcp）：把网关自身的管理能力暴露为独立 MCP 服务器。
	// 与上面的聚合入口互不相干：工具由本进程实现，鉴权走独立管理令牌。
	adminCfg, err := loadAdminMCPConfig(st)
	if err != nil {
		slog.Error("读取管理 MCP 配置失败，按未启用处理", "err", err)
	}
	adminSrv := adminmcp.NewServer(adminCfg)
	adminmcp.RegisterReadTools(adminSrv, adminmcp.ReadDeps{Store: st, Pricer: pricer})
	adminmcp.RegisterWriteTools(adminSrv, adminmcp.WriteDeps{
		Store: st, Host: host, MCP: mcpSvc,
		HTTP: &http.Client{Timeout: 60 * time.Second},
	})
	adminMCPHandler := adminmcp.NewHandler(adminSrv)
	api.SetAdminMCP(adminSrv)

	if a.cfg.AdminPassword != "" {
		if initialized, err := api.Service().IsInitialized(); err == nil && !initialized {
			if err := api.Service().Setup(a.cfg.AdminPassword); err != nil {
				slog.Error("初始化管理员密码失败", "err", err)
			} else {
				slog.Info("已通过 --admin-password 完成首次初始化")
			}
		}
	}

	router := httpapi.NewRouter(httpapi.Deps{
		Proxy:    proxyHandler,
		Admin:    api,
		Host:     host,
		MCP:      mcpHandler,
		AdminMCP: adminMCPHandler,
	})
	srv := &http.Server{Addr: addr, Handler: router, ReadHeaderTimeout: 15 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())

	a.mu.Lock()
	a.store, a.host, a.mcpSvc, a.srv, a.cancel, a.running = st, host, mcpSvc, srv, cancel, true
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

	// 价格快照：启动后立刻尝试一次，之后按设置的间隔复查（同一周期内只拉一次，失败沿用现有价格表）。
	// 面板改了间隔或价格源地址后，下一轮复查即按新值执行，无需重启。
	go func() {
		client := &http.Client{Timeout: 45 * time.Second}
		interval := priceInterval(runtime)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			pricer.RefreshIfDue(ctx, client, a.priceEndpoint(), interval)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if next := priceInterval(a.loadRuntimeSettings()); next != interval {
				interval = next
				ticker.Reset(interval)
			}
		}
	}()

	return nil
}

// loadRuntimeSettings 读取运行期设置；失败时回退为默认值。
func (a *App) loadRuntimeSettings() store.RuntimeSettings {
	if a.store == nil {
		return store.DefaultRuntimeSettings()
	}
	rs, err := a.store.LoadRuntimeSettings()
	if err != nil {
		return store.DefaultRuntimeSettings()
	}
	return rs
}

// loadAdminMCPConfig 读取管理 MCP 的开关、写权限与令牌摘要。
func loadAdminMCPConfig(st *store.Store) (*adminmcp.Config, error) {
	var enabled, writable bool
	var hash string

	if v, ok, err := st.GetSetting(store.SettingAdminMCPEnabled); err != nil {
		return adminmcp.NewConfig(false, false, ""), err
	} else if ok {
		enabled = v == "1"
	}
	if v, ok, err := st.GetSetting(store.SettingAdminMCPWritable); err != nil {
		return adminmcp.NewConfig(enabled, false, ""), err
	} else if ok {
		writable = v == "1"
	}
	if v, ok, err := st.GetSetting(store.SettingAdminMCPTokenHash); err != nil {
		return adminmcp.NewConfig(enabled, writable, ""), err
	} else if ok {
		hash = v
	}
	return adminmcp.NewConfig(enabled, writable, hash), nil
}

// priceEndpoint 返回生效的价格源地址：面板设置优先，其次环境变量 / 内置默认。
func (a *App) priceEndpoint() string {
	if rs := a.loadRuntimeSettings(); rs.PriceEndpoint != "" {
		return rs.PriceEndpoint
	}
	return pricing.Endpoint()
}

// priceInterval 把「价格刷新间隔（分钟）」换算为时长，非法值回退为 1 小时。
func priceInterval(rs store.RuntimeSettings) time.Duration {
	if rs.PriceRefreshMinutes <= 0 {
		return time.Hour
	}
	return time.Duration(rs.PriceRefreshMinutes) * time.Minute
}

// Stop 停止网关：关闭 HTTP 服务、回收插件进程、释放数据库。
func (a *App) Stop() error {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return nil
	}
	srv, host, mcpSvc, st, cancel := a.srv, a.host, a.mcpSvc, a.store, a.cancel
	a.store, a.host, a.mcpSvc, a.srv, a.cancel, a.running = nil, nil, nil, nil, nil, false
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
	if mcpSvc != nil {
		mcpSvc.StopLocal()
	}
	if st != nil {
		_ = st.Close()
	}
	return err
}