package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// WorkBuddy 插件：把本机桌面端的账号接进网关面板。
//
// 能力：
//   - 从本机桌面端导入账号（解密其 at-rest 加密的登录态）
//   - OAuth 授权登录（浏览器授权，完成后账号自动入库）
//   - 多账号、签到、额度查询
//   - 对话转发（上游只接受流式：客户端要流式就透传，要非流式就在本地聚合）
//
// 用法：
//
//	workbuddy.exe serve --port 8399 --data <插件数据目录>
//	workbuddy.exe import --data <插件数据目录>     # 命令行导入一次，便于排查
//	workbuddy.exe version
//
// 上游接口路径都可在 <数据目录>/../config.json 里覆盖（见 Endpoints）。
const pluginVersion = "0.4.0"

type Plugin struct {
	eps   Endpoints
	store *accountStore
	http  *http.Client
	chat  *http.Client
	data  string
	oauth *oauthManager
}

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[workbuddy] ")

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
	case "import":
		err = runImport(args)
	case "probe":
		err = runProbe(args)
	case "ask":
		err = runAsk(args)
	case "version", "-v", "--version":
		fmt.Println("workbuddy plugin " + pluginVersion)
	default:
		err = fmt.Errorf("未知命令 %q，可用：serve | import | version", cmd)
	}
	if err != nil {
		log.Printf("退出：%v", err)
		os.Exit(1)
	}
}

// Windows 下宿主用 CTRL_BREAK_EVENT 通知停止，Go 会把它映射成 SIGBREAK(0x15)。
// 部分 Go 版本没有导出这个常量，这里直接给出数值。
const sigBreak = syscall.Signal(0x15)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", 0, "监听端口（默认取 AI_GATEWAY_PLUGIN_PORT）")
	dataDir := fs.String("data", "", "插件数据目录（凭据与缓存）")
	configPath := fs.String("config", "", "接口配置覆盖文件")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *port == 0 {
		*port = envInt("AI_GATEWAY_PLUGIN_PORT", 8399)
	}
	if *dataDir == "" {
		*dataDir = defaultDataDir()
	}
	if *configPath == "" {
		*configPath = filepath.Join(filepath.Dir(execPath()), "config.json")
	}

	eps, err := loadEndpoints(*configPath)
	if err != nil {
		return err
	}
	store, err := newAccountStore(*dataDir)
	if err != nil {
		return err
	}
	p := &Plugin{
		eps:   eps,
		store: store,
		data:  *dataDir,
		http:  &http.Client{Timeout: 60 * time.Second},
		chat:  &http.Client{},
	}
	p.oauth = newOAuthManager(p, *configPath)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", p.handleHealth)
	mux.HandleFunc("GET /console/accounts", p.handleAccountsList)
	mux.HandleFunc("POST /console/accounts", p.handleAccountAdd)
	mux.HandleFunc("DELETE /console/accounts/{id}", p.handleAccountDelete)
	mux.HandleFunc("POST /console/accounts/{id}/checkin", p.handleCheckin)
	mux.HandleFunc("POST /console/accounts/{id}/quota", p.handleQuota)
	mux.HandleFunc("POST /console/login/{method}", p.handleLoginStart)
	mux.HandleFunc("GET /v1/models", p.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", p.forwardChat)
	p.oauth.registerRoutes(mux)

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

	// 自动签到：启动补签 + 每小时复查
	p.startAutoCheckin(ctx)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Printf("收到停止信号，正在退出")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func runImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	dataDir := fs.String("data", "", "插件数据目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" {
		*dataDir = defaultDataDir()
	}

	accounts, notes, err := ImportFromDesktop()
	for _, n := range notes {
		log.Printf("提示：%s", n)
	}
	if err != nil {
		return err
	}

	store, err := newAccountStore(*dataDir)
	if err != nil {
		return err
	}
	for _, a := range accounts {
		_, created := store.upsert(a)
		action := "更新"
		if created {
			action = "新增"
		}
		log.Printf("%s账号 %s（%s，uid=%s）token=%s 到期=%s",
			action, a.Name, siteLabel(a.Site), shortUID(a.UID), maskToken(a.AccessToken), formatTime(a.TokenExpiresAt))
	}
	return nil
}

func runProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	blob, err := findKeyBlob()
	if err != nil {
		return err
	}
	keyID, err := blob.ProtectorKeyID()
	if err != nil {
		return err
	}
	log.Printf("目标 protectorKeyId=%s（来自 %s）", keyID, blob.Path)

	lines, err := probeProcesses(keyID)
	if err != nil {
		return err
	}
	for _, l := range lines {
		log.Printf("%s", l)
	}
	return nil
}

func (p *Plugin) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{
		"status":   "ok",
		"version":  pluginVersion,
		"accounts": len(p.store.list()),
	})
}

func loadEndpoints(path string) (Endpoints, error) {
	eps := defaultEndpoints()
	if strings.TrimSpace(path) == "" {
		return eps, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return eps, nil
		}
		return eps, fmt.Errorf("读取接口配置失败: %w", err)
	}
	var user Endpoints
	if err := json.Unmarshal(raw, &user); err != nil {
		return eps, fmt.Errorf("解析接口配置 %s 失败: %w", path, err)
	}
	// 只覆盖用户显式写过的字段，其余保持默认
	dst := reflect.ValueOf(&eps).Elem()
	src := reflect.ValueOf(user)
	for i := 0; i < src.NumField(); i++ {
		if src.Field(i).Kind() != reflect.String {
			continue
		}
		if v := strings.TrimSpace(src.Field(i).String()); v != "" {
			dst.Field(i).SetString(v)
		}
	}
	return eps, nil
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

// maskToken 只保留头尾，避免日志里出现完整凭据。
func maskToken(tok string) string {
	if len(tok) <= 12 {
		return "***"
	}
	return tok[:6] + "***" + tok[len(tok)-4:]
}

func formatTime(ms int64) string {
	if ms <= 0 {
		return "未知"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04")
}