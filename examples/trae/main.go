package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// TRAE 插件：把 TRAE SOLO 通道包装成一个 OpenAI 兼容的上游，供 AI Gateway 按「账号即密钥」调度。
//
// 能力：
//   - 实现 GET /health、POST /v1/chat/completions、GET /v1/models
//   - 控制台：账号列表 / 网页登录（临时回调端口）/ 手工导入 / 删除 / 签到 / 额度刷新
//   - 按 X-Plugin-Account 精确选号（选择哪个账号全部归网关，插件不做任何调度）
//
// 用法：
//
//	trae-plugin.exe serve --port <AI_GATEWAY_PLUGIN_PORT> --data <插件数据目录>
//	trae-plugin.exe version
//
// 上游主机与接口路径都可在 exe 同级 config.json 里覆盖（见 Endpoints）。
const pluginVersion = "1.0.0"

type Plugin struct {
	eps    Endpoints
	store  *accountStore
	api    *http.Client // 短 JSON 请求（带总超时）
	stream *http.Client // 流式对话（无总超时，仅首字节兜底）
	login  *loginBroker
	data   string
	// dumpDir 非空时，把每个 /v1/chat/completions 收到的原始请求体全量落盘（诊断用）。
	// 由环境变量 TRAE_DUMP_DIR 或 --dump 开关设置；默认关闭。
	dumpDir string
}

// Windows 下宿主用 CTRL_BREAK_EVENT 通知停止，Go 会把它映射成 SIGBREAK(0x15)。
// 部分 Go 版本没有导出这个常量，这里直接给出数值。
const sigBreak = syscall.Signal(0x15)

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[trae] ")

	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}

	var err error
	switch cmd {
	case "serve", "run":
		err = runServe(args)
	case "version", "-v", "--version":
		fmt.Println("trae plugin " + pluginVersion)
	default:
		err = fmt.Errorf("未知命令 %q，可用：serve | version", cmd)
	}
	if err != nil {
		log.Printf("退出：%v", err)
		os.Exit(1)
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", 0, "监听端口（默认取 AI_GATEWAY_PLUGIN_PORT）")
	dataDir := fs.String("data", "", "插件数据目录（凭据与状态）")
	configPath := fs.String("config", "", "上游接口配置覆盖文件")
	dump := fs.String("dump", "", "诊断：把收到的原始请求体全量写入该目录（默认关闭，也可用 TRAE_DUMP_DIR）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *port == 0 {
		*port = envInt("AI_GATEWAY_PLUGIN_PORT", 20727)
	}
	if *dataDir == "" {
		*dataDir = defaultDataDir()
	}
	if *configPath == "" {
		*configPath = filepath.Join(filepath.Dir(execPath()), "config.json")
	}
	dumpDir := strings.TrimSpace(*dump)
	if dumpDir == "" {
		dumpDir = strings.TrimSpace(os.Getenv("TRAE_DUMP_DIR"))
	}
	if dumpDir != "" {
		if err := os.MkdirAll(dumpDir, 0o755); err != nil {
			return fmt.Errorf("创建 dump 目录失败：%w", err)
		}
		log.Printf("已开启请求体 dump：%s", dumpDir)
	}

	eps, err := loadEndpoints(*configPath)
	if err != nil {
		return err
	}
	store, err := newAccountStore(*dataDir)
	if err != nil {
		return err
	}

	tr := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second, // 首字节兜底，不限制整流时长
	}
	p := &Plugin{
		eps:     eps,
		store:   store,
		data:    *dataDir,
		dumpDir: dumpDir,
		api:     &http.Client{Timeout: 60 * time.Second, Transport: tr},
		stream:  &http.Client{Transport: tr},
	}
	p.login = newLoginBroker(p)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", p.handleHealth)
	mux.HandleFunc("GET /healthz", p.handleHealth) // 直连调试用的别名
	mux.HandleFunc("GET /v1/models", p.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", p.forwardChat)
	mux.HandleFunc("GET /console/accounts", p.handleAccountsList)
	mux.HandleFunc("POST /console/accounts", p.handleAccountAdd)
	mux.HandleFunc("DELETE /console/accounts/{id}", p.handleAccountDelete)
	mux.HandleFunc("POST /console/accounts/{id}/checkin", p.handleCheckin)
	mux.HandleFunc("POST /console/accounts/{id}/quota", p.handleQuota)
	mux.HandleFunc("POST /console/login/{method}", p.handleLoginStart)
	mux.HandleFunc("GET /console/login/{method}", p.handleLoginStatus)

	srv := &http.Server{
		Addr:              "127.0.0.1:" + strconv.Itoa(*port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, sigBreak)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("监听 http://127.0.0.1:%d（数据目录 %s，账号 %d 个）", *port, *dataDir, len(store.list()))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// 后台任务：token 到期前 24 小时预刷新（不做额度定时轮询）。
	// 自动签到由网关宿主调度，插件只提供 POST /console/accounts/{id}/checkin。
	p.startBackground(ctx)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Printf("收到停止信号，正在退出")
		p.login.closeAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// handleHealth 探活：**恒返回 200**，绝不探测上游/账号状态（规范 §7）。
func (p *Plugin) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{
		"status":   "ok",
		"version":  pluginVersion,
		"accounts": len(p.store.list()),
	})
}

// ---------- 配置小工具 ----------

func defaultModel() string {
	if v := strings.TrimSpace(os.Getenv("TRAE_DEFAULT_MODEL")); v != "" {
		return v
	}
	return "glm-5.2"
}

func defaultDataDir() string {
	if v := strings.TrimSpace(os.Getenv("AI_GATEWAY_PLUGIN_DATA")); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(execPath()), "data")
}

func execPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return exe
}

func envInt(name string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// ---------- 脱敏 ----------

// tokenLike 匹配疑似凭据的长串（连续 24+ 个 URL 安全字符），用于 error 脱敏。
var tokenLike = regexp.MustCompile(`[A-Za-z0-9_\-./+=]{24,}`)

// sanitizeError 脱敏错误文案：抹掉疑似 token 片段并压缩到 100 字内，
// 保证 last_error 里绝不出现凭据（规范 §8）。
func sanitizeError(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	msg = tokenLike.ReplaceAllString(msg, "***")
	return truncateRunes(msg, 100)
}

// clampBytes 把字符串截断到不超过 n 字节（按 rune 边界切，避免截出半个汉字）。
// OpenAI 入口只透传响应体前 300 字节（规范 §5.3），所以错误文案必须压在这个量级内。
func clampBytes(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	total := 0
	for i, ch := range r {
		size := len(string(ch))
		if total+size > n {
			return string(r[:i])
		}
		total += size
	}
	return s
}