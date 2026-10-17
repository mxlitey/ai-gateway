package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/config"
)

func TestValidatePythonPlugin(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "pyhello", `{
  "name": "PyHello",
  "entry": "main.py",
  "port": 20801,
  "requirements": ["httpx>=0.27"]
}`, "main.py")

	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("加载清单失败: %v", err)
	}
	if err := Validate(dir, m); err != nil {
		t.Fatalf("Python 插件应校验通过，实际: %v", err)
	}
	if !m.HasRequirements() {
		t.Fatal("应识别出依赖声明")
	}
}

// TestValidateRejectsNonPythonEntry 网关只支持 Python 插件：非 .py 入口必须被拒绝。
func TestValidateRejectsNonPythonEntry(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "pyhello", `{
  "name": "PyHello", "entry": "main.exe", "port": 20801
}`, "main.exe")

	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = Validate(dir, m)
	if err == nil || !strings.Contains(err.Error(), ".py") {
		t.Fatalf("非 .py 入口应被拒绝并提示 .py，实际: %v", err)
	}
}

func TestPipSourcesOfficialFirst(t *testing.T) {
	got := pipSources(pyEnv{})
	if len(got) == 0 || got[0] != "https://pypi.org/simple" {
		t.Fatalf("默认应官方源优先，实际: %v", got)
	}
	// 官方源之后必须是国内镜像，顺序固定
	if !strings.Contains(strings.Join(got, ","), "pypi.tuna.tsinghua.edu.cn") {
		t.Fatalf("内置列表应包含国内镜像，实际: %v", got)
	}
}

func TestPipSourcesCustomOverrides(t *testing.T) {
	got := pipSources(pyEnv{IndexURLs: []string{"https://mirror.internal/simple", "https://mirror.internal/simple"}})
	if len(got) != 1 || got[0] != "https://mirror.internal/simple" {
		t.Fatalf("自定义源应去重且不追加默认列表，实际: %v", got)
	}
}

func TestPipLockCache(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".pip.lock")
	hash := requirementsHash([]string{"httpx>=0.27"})

	if samePyDeps(lockPath, hash, "3.12.7") {
		t.Fatal("没有 lock 时不应认为依赖已就绪")
	}

	writePipLock(lockPath, pyPipLock{
		RequirementsHash: hash,
		IndexURL:         "https://pypi.org/simple",
		UpdatedAt:        time.Now().UnixMilli(),
		Version:          "3.12.7",
	})
	if !samePyDeps(lockPath, hash, "3.12.7") {
		t.Fatal("同指纹同版本应命中缓存")
	}
	if samePyDeps(lockPath, hash, "3.13.0") {
		t.Fatal("解释器版本变化后必须重装依赖")
	}
	if samePyDeps(lockPath, requirementsHash([]string{"httpx>=0.28"}), "3.12.7") {
		t.Fatal("依赖变化后必须重装")
	}
	if got := cachedPipSource(lockPath, hash, "3.12.7"); got != "https://pypi.org/simple" {
		t.Fatalf("应返回上次成功的源，实际 %q", got)
	}
}

func TestSafeJoinRejectsEscape(t *testing.T) {
	dest := t.TempDir()
	if _, err := safeJoin(dest, "../evil"); err == nil {
		t.Fatal("越界路径应被拒绝")
	}
	if _, err := safeJoin(dest, "python/bin/python3"); err != nil {
		t.Fatalf("正常路径应放行: %v", err)
	}
}

func TestPortablePythonDetected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("便携目录布局在 Unix 上验证")
	}
	root := t.TempDir()
	// 造一个假的便携解释器：/bin/python3，能响应 -c 打印版本
	binDir := filepath.Join(root, "3.12", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '3.12.7'\n"
	bin := filepath.Join(binDir, "python3")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	h := &Host{cfg: &config.Config{DataDir: t.TempDir()}}
	interp := h.findPortablePython(pyEnv{RuntimeDir: root})
	if interp == nil {
		t.Fatal("应探测到便携解释器")
	}
	if !interp.Portable || interp.Version != "3.12.7" {
		t.Fatalf("解释器信息错误: %+v", interp)
	}
}

func TestPyVersionOrder(t *testing.T) {
	if !pyVersionLess("3.9", "3.12") {
		t.Fatal("3.9 应低于 3.12（按数值而非字典序）")
	}
	if pyVersionLess("3.12", "3.9") {
		t.Fatal("比较结果不应反转")
	}
}

func TestPyReleaseTargetAndURL(t *testing.T) {
	target, err := pyReleaseTarget()
	if err != nil {
		t.Skipf("当前平台不支持便携 Python: %v", err)
	}
	u := pyReleaseAssetURL("3.12", target)
	if !strings.HasSuffix(u, "-install_only.tar.gz") || !strings.Contains(u, target) {
		t.Fatalf("发行包地址形状错误: %s", u)
	}
}
