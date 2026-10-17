# AI Gateway 绿色版一键构建
# 用法：pwsh -File build/build.ps1 -Version 0.1.0
param(
    [string]$Version = "dev"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

function Require-Command([string]$name, [string]$hint) {
    if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
        Write-Host "[错误] 未找到 $name。$hint" -ForegroundColor Red
        exit 1
    }
}

Require-Command "go" "请安装 Go 1.23+：winget install GoLang.Go"

$module = "github.com/czg95/ai-gateway-v2"

# ---- 1. 前端构建 ----
Write-Host "[1/4] 构建前端 ..." -ForegroundColor Cyan
if (Test-Path "web/package.json") {
    Require-Command "node" "请安装 Node.js 20+"
    # 清掉上一次的构建产物（保留 .gitkeep，供 embed 使用），避免旧资源被打进二进制
    if (Test-Path "web/dist") {
        Get-ChildItem "web/dist" -Force | Where-Object { $_.Name -ne ".gitkeep" } | Remove-Item -Recurse -Force
    }
    Push-Location web
    try {
        if (-not (Test-Path "node_modules")) { npm install }
        npm run build
    } finally {
        Pop-Location
    }
} else {
    Write-Host "  web/package.json 不存在，跳过前端构建（将使用现有 web/dist）" -ForegroundColor Yellow
}

# ---- 2. Go 依赖 ----
Write-Host "[2/4] 解析 Go 依赖 ..." -ForegroundColor Cyan
# 国内网络默认走不通 proxy.golang.org，未显式配置时回落到国内镜像
if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn,direct" }
go mod tidy

# ---- 3. 编译 ----
Write-Host "[3/4] 编译可执行文件 ..." -ForegroundColor Cyan
$commit = "none"
try { $commit = (git rev-parse --short HEAD 2>$null) } catch { }
if (-not $commit) { $commit = "none" }
$buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
New-Item -ItemType Directory -Force -Path dist | Out-Null

$ldflags = "-s -w " +
    "-X $module/internal/version.Version=$Version " +
    "-X $module/internal/version.Commit=$commit " +
    "-X $module/internal/version.BuildTime=$buildTime"

# 只产出桌面窗口版（ai-gateway.exe）
Write-Host "  生成桌面端 bindings ..." -ForegroundColor Cyan
$wails3 = Join-Path (go env GOPATH) "bin\wails3.exe"
if (Test-Path $wails3) {
    Push-Location "cmd/gateway-wails"
    try {
        & $wails3 generate bindings -clean -d assets/bindings | Out-Null
        # 图标、DPI 清单与版本信息（决定任务管理器里的显示名）：生成 .syso，Go 构建时自动链接
        if ((Test-Path "appicon.ico") -and (Test-Path "wails.exe.manifest")) {
            $sysoArgs = @("generate", "syso", "-manifest", "wails.exe.manifest", "-icon", "appicon.ico", "-arch", "amd64")
            if (Test-Path "info.json") { $sysoArgs += @("-info", "info.json") }
            $sysoArgs += @("-out", "icon_windows_amd64.syso")
            & $wails3 @sysoArgs | Out-Null
        }
    } finally { Pop-Location }
} else {
    Write-Host "  未找到 wails3，跳过 bindings/syso 生成（沿用已提交的产物）" -ForegroundColor Yellow
}
go build -trimpath -ldflags "$ldflags -H=windowsgui" -o dist/ai-gateway.exe ./cmd/gateway-wails
Remove-Item "dist/ai-gateway.exe~" -Force -ErrorAction SilentlyContinue

# ---- 4. 组装绿色版 ----
Write-Host "[4/4] 组装绿色版 ..." -ForegroundColor Cyan
Copy-Item "build/README.txt" "dist/README.txt" -Force
New-Item -ItemType Directory -Force -Path "dist/plugins", "dist/data" | Out-Null

# 内置插件：插件一律是 Python 源码（同一份源码在 Windows / macOS / Linux 通用），
# 整目录拷贝即可，不需要任何编译。先删旧目录，避免残留上一次的可执行产物。
foreach ($name in @("trae", "workbuddy", "hello-py")) {
    if (-not (Test-Path "examples/$name/plugin.json")) { continue }
    $dst = "dist/plugins/$name"
    Remove-Item $dst -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path $dst | Out-Null
    Copy-Item "examples/$name/*.py" $dst -Force
    Copy-Item "examples/$name/plugin.json" "$dst/plugin.json" -Force
}

Write-Host ""
Write-Host "构建完成: dist/ai-gateway.exe ($Version)" -ForegroundColor Green
Write-Host "双击 dist/ai-gateway.exe 即可启动并打开管理面板" -ForegroundColor Green