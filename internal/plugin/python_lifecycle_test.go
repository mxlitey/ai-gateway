package plugin

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/config"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// TestPythonPluginLifecycle 端到端验证：宿主解析系统 Python、拉起插件、
// 探活通过并注册渠道，最后随宿主退出被回收。
//
// 本机没有 Python 3 时跳过（便携运行时需要下载，不适合放在单测里）。
func TestPythonPluginLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("端到端用例，-short 下跳过")
	}
	if findSystemPython() == nil {
		t.Skip("本机没有可用的 Python 3")
	}

	root := t.TempDir()
	pluginDir := filepath.Join(root, "plugins", "pylife")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	port := freePort(t)
	manifest := `{
  "name": "PyLife",
  "entry": "main.py",
  "port": ` + strconv.Itoa(port) + `,
  "startup_timeout_ms": 30000
}`
	script := `import json, os
from http.server import BaseHTTPRequestHandler, HTTPServer

class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass
    def do_GET(self):
        body = json.dumps({"status": "ok", "account": self.headers.get("X-Plugin-Account", "")}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

HTTPServer(("127.0.0.1", int(os.environ["AI_GATEWAY_PLUGIN_PORT"])), H).serve_forever()
`
	if err := os.WriteFile(filepath.Join(pluginDir, manifestFileName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "main.py"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	cfg := &config.Config{DataDir: filepath.Join(root, "data"), PluginsDir: filepath.Join(root, "plugins")}
	host := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer host.Shutdown()

	if err := host.Discover(); err != nil {
		t.Fatalf("扫描插件失败: %v", err)
	}

	// 等待进入 running（探活通过后宿主才同步渠道）
	deadline := time.Now().Add(30 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		for _, p := range host.List() {
			if p.ID == "pylife" && p.LastStatus == StatusRunning {
				ready = true
				break
			}
		}
		if ready {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		for _, p := range host.List() {
			t.Logf("插件 %s 状态=%s 错误=%s", p.ID, p.LastStatus, p.LastError)
		}
		t.Fatal("插件未在 30 秒内进入 running")
	}

	// 渠道已注册且指向插件端口
	channels, err := st.ListChannels()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ch := range channels {
		if ch.Source == "plugin:pylife" {
			found = true
			if ch.BaseURL != UpstreamBaseURL(port) {
				t.Fatalf("渠道上游地址错误: %s", ch.BaseURL)
			}
			if !ch.Enabled {
				t.Fatal("运行中的插件渠道应处于启用状态")
			}
		}
	}
	if !found {
		t.Fatal("未注册插件渠道")
	}

	// 请求插件：确认宿主拉起的进程确实在监听
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
	if err != nil {
		t.Fatalf("插件未响应: %v", err)
	}
	_ = resp.Body.Close()

	// 停止插件：状态回到 stopped，渠道被停用
	if err := host.Stop("pylife"); err != nil {
		t.Fatalf("停止插件失败: %v", err)
	}
	for _, p := range host.List() {
		if p.ID == "pylife" && p.LastStatus != StatusStopped {
			t.Fatalf("停止后状态应为 stopped，实际 %s", p.LastStatus)
		}
	}
	channels, _ = st.ListChannels()
	for _, ch := range channels {
		if ch.Source == "plugin:pylife" && ch.Enabled {
			t.Fatal("插件停止后渠道应被停用")
		}
	}
}

// freePort 返回一个当前空闲的回环端口。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取空闲端口失败: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestPythonCandidatesPreferLauncher Windows 上必须优先用 py 启动器，避免命中应用商店占位。
func TestPythonCandidatesPreferLauncher(t *testing.T) {
	got := systemPythonCandidates()
	if runtime.GOOS == "windows" {
		if len(got) == 0 || got[0][0] != "py" || len(got[0]) != 2 || got[0][1] != "-3" {
			t.Fatalf("Windows 应优先使用 py -3，实际 %v", got)
		}
		return
	}
	if len(got) == 0 || got[0][0] != "python3" {
		t.Fatalf("Unix 应优先使用 python3，实际 %v", got)
	}
}
