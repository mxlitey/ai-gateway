# AI Gateway 绿色版一键构建
# 用法：pwsh -File build/build.ps1 -Version 1.2.0
#       加 -Docker 同时构建 Linux 镜像（默认 amd64 + arm64 双架构），
#       导出 dist/ai-gateway-v<版本>-docker-amd64.tar 与 -arm64.tar
param(
    [string]$Version = "dev",
    # 额外构建 Docker 镜像（debian:bookworm-slim，内置 trae / workbuddy 等 9 个插件）
    [switch]$Docker,
    # Docker 目标架构；只想构建单架构时传 -DockerPlatforms linux/amd64
    [string[]]$DockerPlatforms = @("linux/amd64", "linux/arm64"),
    # apt 镜像站，国内默认阿里云；传空串则用 debian 官方源
    [string]$AptMirror = "mirrors.aliyun.com"
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
$totalSteps = if ($Docker) { 5 } else { 4 }

# ---- 1. 前端构建 ----
Write-Host "[1/$totalSteps] 构建前端 ..." -ForegroundColor Cyan
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
Write-Host "[2/$totalSteps] 解析 Go 依赖 ..." -ForegroundColor Cyan
# 国内网络默认走不通 proxy.golang.org，未显式配置时回落到国内镜像
if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn,direct" }
go mod tidy

# ---- 3. 编译 ----
Write-Host "[3/$totalSteps] 编译可执行文件 ..." -ForegroundColor Cyan
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

# 只产出桌面窗口版（ai-gateway-v<版本>.exe）
$exe = "dist/ai-gateway-v$Version.exe"
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
go build -trimpath -ldflags "$ldflags -H=windowsgui" -o $exe ./cmd/gateway-wails
Remove-Item "$exe~" -Force -ErrorAction SilentlyContinue
# 清理旧的无版本号产物，避免与新版命名混淆
Remove-Item "dist/ai-gateway.exe" -Force -ErrorAction SilentlyContinue

# ---- 4. 组装绿色版 ----
Write-Host "[4/$totalSteps] 组装绿色版 ..." -ForegroundColor Cyan
Copy-Item "build/README.txt" "dist/README.txt" -Force
New-Item -ItemType Directory -Force -Path "dist/plugins", "dist/data" | Out-Null

# 内置插件：插件一律是 Python 源码（同一份源码在 Windows / macOS / Linux 通用），
# 整目录拷贝即可，不需要任何编译。先删旧目录，避免残留上一次的可执行产物。
foreach ($name in @("trae", "workbuddy", "modelscope", "kilo", "cline", "minimax", "loomy", "lobsterai", "gemini")) {
    if (-not (Test-Path "examples/$name/plugin.json")) { continue }
    $dst = "dist/plugins/$name"
    Remove-Item $dst -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path $dst | Out-Null
    Copy-Item "examples/$name/*.py" $dst -Force
    Copy-Item "examples/$name/plugin.json" "$dst/plugin.json" -Force
}

# ---- 5. Docker 镜像（可选）----
if ($Docker) {
    Write-Host "[5/$totalSteps] 构建 Docker 镜像（$($DockerPlatforms -join ' / ')）..." -ForegroundColor Cyan
    Require-Command "docker" "请安装并启动 Docker Desktop"

    # 镜像内嵌前端产物与 Go 二进制都在 Dockerfile 里自己构建，这里只需传版本号与 apt 源。
    # 每个架构单独 buildx --load 并导出独立 tar，目标机器 docker load 后即可运行。
    $tars = @()
    foreach ($platform in $DockerPlatforms) {
        $arch = switch -Regex ($platform) {
            "amd64" { "amd64" }
            "arm64" { "arm64" }
            "arm/v7" { "armv7" }
            default { ($platform -replace "^linux/", "") -replace "[/.]", "-" }
        }
        $image = "ai-gateway:v$Version-$arch"
        $tags = @("-t", $image)
        # amd64 额外打 latest，方便本机 docker run / compose 直接使用
        if ($arch -eq "amd64") { $tags += @("-t", "ai-gateway:latest") }

        Write-Host "  构建 $platform -> $image ..." -ForegroundColor Cyan
        $buildArgs = @("buildx", "build", "--platform", $platform, "--progress=plain") +
                     $tags +
                     @("--build-arg", "VERSION=$Version",
                       # 显式传目标架构：部分 buildx 驱动不会自动注入 TARGETARCH，
                       # 漏传会把 arm64 镜像里的 Go 二进制编成 amd64（启动即 exec format error）
                       "--build-arg", "TARGETARCH=$arch",
                       "--build-arg", "APT_MIRROR=$AptMirror", "--load", ".")
        & docker @buildArgs
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[错误] Docker 镜像构建失败（$platform）" -ForegroundColor Red
            if ($platform -match "arm64|arm/") {
                Write-Host "  arm 架构需要 QEMU 模拟，可先执行：" -ForegroundColor Yellow
                Write-Host "  docker run --privileged --rm tonistiigi/binfmt --install arm64" -ForegroundColor Yellow
            }
            exit 1
        }

        # 导出为离线可分发的 tar；用 -o 而不是 >，避免 PowerShell 按 UTF-16 写坏文件
        $tar = "dist/ai-gateway-v$Version-docker-$arch.tar"
        Write-Host "  导出镜像 -> $tar ..." -ForegroundColor Cyan
        & docker save $image -o $tar
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[错误] docker save 失败（$platform）" -ForegroundColor Red
            exit 1
        }
        $sizeMB = [math]::Round((Get-Item $tar).Length / 1MB, 1)
        Write-Host "  已导出: $tar ($sizeMB MB)" -ForegroundColor Green
        $tars += $tar
    }
}

Write-Host ""
Write-Host "构建完成: $exe ($Version)" -ForegroundColor Green
Write-Host "双击 $exe 即可启动并打开管理面板" -ForegroundColor Green
if ($Docker) {
    Write-Host "Docker 镜像（$($DockerPlatforms -join ' / ')）已导出：" -ForegroundColor Green
    foreach ($t in $tars) { Write-Host "  $t" -ForegroundColor Green }
    Write-Host "  目标机器导入: docker load -i <对应架构的 tar>" -ForegroundColor Green
    Write-Host "  首次启动前清空旧插件卷: docker volume rm ai-gateway-plugins -f" -ForegroundColor Yellow
}