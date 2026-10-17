package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePlugin(t *testing.T, root, id, manifest, entry string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建插件目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), []byte(manifest), 0o644); err != nil {
		t.Fatalf("写入清单失败: %v", err)
	}
	if entry != "" {
		if err := os.WriteFile(filepath.Join(dir, entry), []byte("stub"), 0o644); err != nil {
			t.Fatalf("写入入口失败: %v", err)
		}
	}
	return dir
}

const validManifest = `{
  "name": "Hello",
  "version": "1.0.0",
  "entry": "main.py",
  "port": 20800,
  "console": true,
  "capabilities": ["quota", "unknown-cap", "checkin"]
}`

func TestLoadAndValidateValidPlugin(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "hello", validManifest, "main.py")

	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("加载清单失败: %v", err)
	}
	if err := Validate(dir, manifest); err != nil {
		t.Fatalf("应校验通过，实际: %v", err)
	}
	if manifest.ID != "hello" {
		t.Fatalf("id 应由目录名推导为 hello，实际 %q", manifest.ID)
	}
	if manifest.StartupTimeoutMS != defaultStartupTimeout {
		t.Fatalf("默认启动超时应为 %d，实际 %d", defaultStartupTimeout, manifest.StartupTimeoutMS)
	}
	if !manifest.HasConsole() {
		t.Fatal("声明 console=true 后应判定为提供控制台")
	}
	if manifest.ConsoleBasePath() != consolePath {
		t.Fatalf("控制台前缀应固定为 %s，实际 %q", consolePath, manifest.ConsoleBasePath())
	}
	got := manifest.KnownCapabilities()
	if len(got) != 2 || got[0] != CapCheckin || got[1] != CapQuota {
		t.Fatalf("能力解析错误（未知值应忽略）：%v", got)
	}
}

func TestUpstreamBaseURL(t *testing.T) {
	if got := UpstreamBaseURL(20800); got != "http://127.0.0.1:20800/v1" {
		t.Fatalf("上游根地址错误：%s", got)
	}
}

func TestValidateRejectsBadID(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "Hello", `{"name":"H","entry":"main.py","port":20800}`, "main.py")
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(dir, manifest); err == nil {
		t.Fatal("目录名含大写字母时应报错")
	}
}

func TestValidateRejectsEntryEscape(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "hello", validManifest, "")
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Entry = "../evil.py"

	if err := Validate(dir, manifest); err == nil {
		t.Fatal("entry 逃出插件目录时应报错")
	} else if !strings.Contains(err.Error(), "逃出") {
		t.Fatalf("错误信息应说明路径逃逸，实际: %v", err)
	}
}

func TestValidateRejectsAbsoluteEntry(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "hello", validManifest, "")
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Entry = filepath.Join(dir, "main.py")
	if err := Validate(dir, manifest); err == nil {
		t.Fatal("entry 为绝对路径时应报错")
	}
}

func TestValidateRejectsMissingEntry(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "hello", validManifest, "")
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(dir, manifest); err == nil {
		t.Fatal("entry 文件不存在时应报错")
	}
}

func TestValidateRejectsBadPort(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "hello", `{"name":"H","entry":"main.py"}`, "main.py")
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(dir, manifest); err == nil {
		t.Fatal("未声明 port 时应报错")
	}
}

func TestSubstitute(t *testing.T) {
	vars := substVars{Port: 20001, Data: `C:\data\p`}
	got := substitute("serve --port {{port}} --data {{data}}", vars)
	want := `serve --port 20001 --data {{data}}`
	if got != want {
		t.Fatalf("只应替换 {{port}}\n实际: %s\n期望: %s", got, want)
	}
	if substitute("{{unknown}}", vars) != "{{unknown}}" {
		t.Fatal("未知占位符应原样保留")
	}
}

func TestIsInside(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "sub", "a.exe")
	outside := filepath.Join(base, "..", "b.exe")

	if !IsInside(base, inside) {
		t.Fatal("子路径应判定为目录内")
	}
	if IsInside(base, outside) {
		t.Fatal("上级路径不应判定为目录内")
	}
	if !IsInside(base, base) {
		t.Fatal("目录自身应判定为目录内")
	}
}

func TestValidateDirSkipsManifestMissing(t *testing.T) {
	dir := t.TempDir()
	if _, err := ValidateDir(dir); err == nil {
		t.Fatal("缺少 plugin.json 时应报错")
	}
}