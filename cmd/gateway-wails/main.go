// Command gateway-wails 是 AI Gateway 的桌面外壳（Wails v3）。
//
// 设计边界——桌面窗口与 Web 管理面板相互独立：
//   - 桌面窗口：由本进程内的 Wails bindings 直调 Go 内核，不经过 HTTP。
//     因为窗口要负责「启动网关」，不能依赖尚未启动的 HTTP 服务。
//   - Web 管理面板：仍由 internal/httpapi 在 cfg.Port 上提供，浏览器访问，两者互不影响。
package main

import (
	"embed"
	"log/slog"
	"os"
	"os/exec"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:assets
var assets embed.FS

func main() {
	svc := newGatewayService()

	// 先声明，供单实例回调与托盘闭包捕获
	var win *application.WebviewWindow

	app := application.New(application.Options{
		Name:        "AI Gateway",
		Description: "AI Gateway 桌面控制台",
		LogLevel:    slog.LevelInfo,
		Services: []application.Service{
			application.NewService(svc),
		},
		// 单实例：再次双击时不再开新窗口，而是激活已有窗口
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.czg95.ai-gateway.desktop",
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				showWindow(win)
			},
		},
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(assets),
		},
	})

	win = newMainWindow(app, svc)
	svc.attach(app, win)

	// ---- 系统托盘 ----
	tray := app.SystemTray.New()
	tray.SetIcon(appIconPNG())
	tray.SetTooltip("AI Gateway")
	tray.OnClick(func() { showWindow(win) })

	menu := app.NewMenu()
	menu.Add("显示主界面").OnClick(func(*application.Context) { showWindow(win) })
	menu.Add("打开管理面板").OnClick(func(*application.Context) { svc.OpenPanel() })
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) {
		// 先同步停掉网关与插件子进程，再结束进程
		svc.Shutdown()
		app.Quit()
	})
	tray.SetMenu(menu)

	if err := app.Run(); err != nil {
		slog.Error("桌面窗口异常退出", "err", err)
		os.Exit(1)
	}
}

// newMainWindow 创建主窗口。关窗不退出：隐藏到托盘，网关继续在后台运行。
func newMainWindow(app *application.App, svc *GatewayService) *application.WebviewWindow {
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "AI Gateway",
		Width:     520,
		Height:    700,
		MinWidth:  460,
		MinHeight: 560,
		URL:       "/",
		// 无系统标题栏，改用页面内自绘标题栏（见 assets/index.html）。
		// WebView2CompositionHosting 会启用 WebView2 的合成托管 + 非客户区路由，
		// 这正是页面里 --wails-non-client-region 拖拽/按钮生效的前提。
		Frameless: true,
		Windows: application.WindowsWindow{
			WebView2CompositionHosting: true,
		},
	})

	// app.Quit() 直接结束进程、不经过本钩子，所以这里始终拦截关窗
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		win.Hide()
		e.Cancel()
	})
	return win
}

func showWindow(win *application.WebviewWindow) {
	if win == nil {
		return
	}
	win.Restore()
	win.Show()
	win.Focus()
}

// openPath 用资源管理器打开目录，不弹控制台窗口。
func openPath(dir string) {
	_ = exec.Command("explorer", dir).Start()
}

// ---- 窗口控制（自绘标题栏按钮调用）----

// Minimise 最小化窗口。
func (s *GatewayService) Minimise() {
	s.mu.Lock()
	win := s.win
	s.mu.Unlock()
	if win != nil {
		win.Minimise()
	}
}

// CloseWindow 关窗＝隐藏到托盘，网关继续运行。
func (s *GatewayService) CloseWindow() {
	s.mu.Lock()
	win := s.win
	s.mu.Unlock()
	if win != nil {
		win.Hide()
	}
}