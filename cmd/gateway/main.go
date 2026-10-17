// Command gateway 是 AI Gateway 的无界面（headless）入口，面向容器与服务器部署。
//
// 它只做三件事：解析配置 → 启动内核 → 等待退出信号。
// 与桌面外壳 cmd/gateway-wails 平级，二者共用 internal/app 里的同一套装配逻辑；
// 区别在于本入口不依赖任何 GUI（Wails / WebView2），因此可在 Linux / Docker / macOS 中运行，
// 日志输出到 stdout，便于容器日志采集。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/czg95/ai-gateway-v2/internal/app"
	"github.com/czg95/ai-gateway-v2/internal/config"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		slog.Error("解析参数失败", "err", err)
		os.Exit(2)
	}

	// 日志写入 stdout，级别由 config.Leveler() 承载，
	// 可在面板「运行期设置」中热切换（无需重启进程）。
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: config.Leveler()})
	slog.SetDefault(slog.New(handler))

	a := app.New(cfg)
	if err := a.Start(); err != nil {
		slog.Error("网关启动失败", "err", err)
		os.Exit(1)
	}

	// 等待退出信号：容器里 docker stop 会发 SIGTERM，本地 Ctrl-C 为 SIGINT。
	// 同时监听内核异常退出通道，避免服务已死却仍挂在这里。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		slog.Info("收到退出信号，正在停止网关")
	case err := <-a.Err():
		slog.Error("网关异常退出", "err", err)
	}

	if err := a.Stop(); err != nil {
		slog.Error("网关停止时出错", "err", err)
		os.Exit(1)
	}
}
