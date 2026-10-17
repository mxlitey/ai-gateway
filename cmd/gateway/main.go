// Command gateway 是 AI Gateway 的命令行入口（无桌面窗口）。
//
// 桌面窗口（cmd/gateway-wails）依赖 Wails + WebView2，只能在 Windows 上构建；
// Linux / macOS / Docker 用这个入口跑后端，管理面板仍通过浏览器访问。
//
//	go run ./cmd/gateway -host 0.0.0.0 -port 8787
//	go run ./cmd/gateway -admin-password xxx -no-browser
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/czg95/ai-gateway-v2/internal/app"
	"github.com/czg95/ai-gateway-v2/internal/config"
	"github.com/czg95/ai-gateway-v2/internal/version"
)

func main() {
	// 去掉子命令（serve），其余参数交给 config.Load 解析（CLI > 环境变量 > 默认值）
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	if len(args) > 0 && (args[0] == "version" || args[0] == "-v" || args[0] == "--version") {
		fmt.Println("ai-gateway " + version.Version)
		return
	}

	cfg, err := config.Load(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "参数错误：", err)
		os.Exit(2)
	}
	// 命令行入口默认不自动打开浏览器（容器 / 服务器场景没有桌面）
	if !flagPresent(args, "no-browser") {
		cfg.NoBrowser = true
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: config.Leveler()})))

	gw := app.New(cfg)
	if err := gw.Start(); err != nil {
		slog.Error("网关启动失败", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-gw.Err():
		slog.Error("网关异常退出", "err", err)
		_ = gw.Stop()
		os.Exit(1)
	case <-ctx.Done():
		slog.Info("收到停止信号，正在退出")
		_ = gw.Stop()
	}
}

// flagPresent 判断命令行里是否显式写了某个布尔开关。
func flagPresent(args []string, name string) bool {
	for _, a := range args {
		if a == "-"+name || a == "--"+name {
			return true
		}
	}
	return false
}
