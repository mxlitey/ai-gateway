# AI Gateway 绿色版一键构建
# 用法：pwsh -File build/build.ps1
#       版本号统一写在仓库根目录的 VERSION 文件，构建时自动同步到各处的元数据；
#       临时覆盖可用 -Version 1.3.4。
#       加 -Docker 同时构建 Linux 镜像（默认 amd64 + arm64 双架构），
#       导出 dist/ai-gateway-v<版本>-docker-amd64.tar 与 -arm64.tar
param(
    # 留空则读取仓库根目录的 VERSION 文件
    [string]$Version = "",
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

# 版本号唯一来源：根目录 VERSION 文件
if (-not $Version) {
    if (-not (Test-Path "VERSION")) {
        Write-Host "[错误] 未找到 VERSION 文件，请写入版本号（如 1.3.3）" -ForegroundColor Red
        exit 1
    }
    $Version = (Get-Content "VERSION" -Raw).Trim()
}
if (-not $Version) {
    Write-Host "[错误] VERSION 文件内容为空" -ForegroundColor Red
    exit 1
}
Write-Host "版本号: $Version（来源：VERSION）" -ForegroundColor Cyan

# 把版本号同步到需要静态版本号的派生文件，避免每次改多处。
# web/package.json 与 package-lock.json 由 npm 一并更新；
# cmd/gateway-wails/info.json 决定 Windows 可执行文件的属性页版本。
function Sync-Version([string]$v) {
    if (Test-Path "web/package.json") {
        Push-Location web
        try {
            & npm version $v --no-git-tag-version --allow-same-version 2>$null | Out-Null
            if ($LASTEXITCODE -ne 0) {
                Write-Host "[警告] web/package.json 版本同步失败" -ForegroundColor Yellow
            }
        } finally { Pop-Location }
    }

    $infoPath = "cmd/gateway-wails/info.json"
    if (Test-Path $infoPath) {
        $info = Get-Content $infoPath -Raw -Encoding UTF8
        $replacement = '${1}' + $v + '${2}'
        $info = [regex]::Replace(
            $info,
            '("(?:file_version|product_version|FileVersion|ProductVersion)"\s*:\s*")[^"]*(")',
            $replacement)
        [IO.File]::WriteAllText(
            (Join-Path (Get-Location) $infoPath),
            $info,
            (New-Object Text.UTF8Encoding($false)))
    }
}

Sync-Version $Version

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
# 清空并重建目录。上一次的网关可能仍在运行（Python 子进程占着目录），
# 删除失败时重试几次并给出提示。
function Reset-Dir([string]$path) {
    for ($i = 0; $i -lt 5; $i++) {
        if (-not (Test-Path $path)) { break }
        Remove-Item $path -Recurse -Force -ErrorAction SilentlyContinue
        if (-not (Test-Path $path)) { break }
        Start-Sleep -Milliseconds 500
    }
    if (Test-Path $path) {
        Write-Host "[警告] 无法清空 $path（可能有旧的网关或插件进程在运行），本次产物可能含旧内容" -ForegroundColor Yellow
    }
    New-Item -ItemType Directory -Force -Path $path | Out-Null
}

Write-Host "[4/$totalSteps] 组装绿色版 ..." -ForegroundColor Cyan
Copy-Item "build/README.txt" "dist/README.txt" -Force
New-Item -ItemType Directory -Force -Path "dist/data" | Out-Null

# 主包只放网关本体：插件与 MCP 全部拆到独立压缩包，两者可分别升级。
$stage = "dist/.stage-windows"
Reset-Dir $stage
Copy-Item "dist/README.txt" $stage -Force
Copy-Item $exe $stage -Force
Get-ChildItem $stage -Recurse -Directory -Filter "__pycache__" -ErrorAction SilentlyContinue |
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue

$winZip = "dist/ai-gateway-v$Version-windows-amd64.zip"
Remove-Item $winZip -Force -ErrorAction SilentlyContinue
Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $winZip -CompressionLevel Optimal
Remove-Item $stage -Recurse -Force -ErrorAction SilentlyContinue

# 插件 + MCP 独立包：解压后直接得到 plugins/ 与 mcp/，与 exe 放同一目录即可被扫描到。
# 插件一律是 Python 源码（同一份源码在 Windows / macOS / Linux 通用），整目录拷贝即可。
$extrasStage = "dist/.stage-extras"
Reset-Dir $extrasStage
New-Item -ItemType Directory -Force -Path "$extrasStage/plugins", "$extrasStage/mcp", "$extrasStage/skills" | Out-Null

foreach ($name in @("trae", "workbuddy")) {
    $src = "examples/plugins/$name"
    if (-not (Test-Path "$src/plugin.json")) { continue }
    $dst = "$extrasStage/plugins/$name"
    New-Item -ItemType Directory -Force -Path $dst | Out-Null
    Copy-Item "$src/*.py" $dst -Force
    Copy-Item "$src/plugin.json" "$dst/plugin.json" -Force
}

$mcpBuiltin = @{
    "github-search"    = "examples/mcp/github-search"
    "image-generation" = "examples/mcp/image-generation"
}
foreach ($name in $mcpBuiltin.Keys) {
    $src = $mcpBuiltin[$name]
    if (-not (Test-Path $src)) { continue }
    $dst = "$extrasStage/mcp/$name"
    New-Item -ItemType Directory -Force -Path $dst | Out-Null
    Copy-Item "$src/*" $dst -Force -Recurse
}
if (Test-Path "examples/skills/image-gen") {
    Copy-Item "examples/skills/image-gen" "$extrasStage/skills/image-gen" -Recurse -Force
}
# 插件与 MCP 都需要 Python 运行；离线环境把 dist/ 里的安装包一并带上（没有则跳过）
Get-ChildItem "dist" -Filter "python-*-amd64.exe" -File -ErrorAction SilentlyContinue |
    ForEach-Object { Copy-Item $_.FullName $extrasStage -Force }
Get-ChildItem $extrasStage -Recurse -Directory -Filter "__pycache__" -ErrorAction SilentlyContinue |
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue

$extrasReadme = @(
    "AI Gateway 插件与 MCP 扩展包（v$Version）",
    "",
    "用法：解压后把 plugins\ 与 mcp\ 放到 ai-gateway 可执行文件所在目录（与 exe 同级），",
    "重启网关即可在面板看到；网关启动时自动扫描这两个目录。",
    "",
    "plugins\  插件目录（trae、workbuddy）。每个子目录一个插件，目录内含 plugin.json 与 Python 源码。",
    "          也可从仓库 examples/plugins/ 复制更多插件进来，或直接在面板里「添加插件」。",
    "mcp\      本地 MCP 服务器（github-search、image-generation）。每个子目录一个服务，",
    "          目录内含 mcp.json 与 Python 入口；在「MCP 服务器」页带「本地」徽章。",
    "skills\   image-gen 技能文档，供支持 skills 的客户端参考（可选，不影响运行）。",
    "python-3.*-amd64.exe  离线用 Python 安装包（如果包含在本包里）。插件与 MCP 都是 Python 程序，",
    "          需要 Python 3.10+；机器上已有 Python 时无需安装。",
    "",
    "插件与 MCP 都是纯 Python 源码，与网关版本解耦：升级网关时无需重新解压本包，",
    "反过来升级本包也不需要更换网关。删除对应目录后重启即从面板移除。",
    "",
    "内置插件与 MCP 的用法见网关包里的 README.txt。"
) -join "`r`n"
[IO.File]::WriteAllText((Join-Path (Get-Location) "$extrasStage/README.txt"), $extrasReadme, (New-Object Text.UTF8Encoding($false)))

$extrasZip = "dist/ai-gateway-v$Version-plugins-mcp.zip"
Remove-Item $extrasZip -Force -ErrorAction SilentlyContinue
Compress-Archive -Path (Join-Path $extrasStage "*") -DestinationPath $extrasZip -CompressionLevel Optimal
Remove-Item $extrasStage -Recurse -Force -ErrorAction SilentlyContinue

# ---- 5. Docker 镜像（可选）----
if ($Docker) {
    Write-Host "[5/$totalSteps] 构建 Docker 镜像（$($DockerPlatforms -join ' / ')）..." -ForegroundColor Cyan
    Require-Command "docker" "请安装并启动 Docker Desktop"

    # 镜像只含网关本体（前端与 Go 二进制都在 Dockerfile 里构建），插件与 MCP 不打进去，
    # 由用户放进 compose 映射的 ./plugins 与 ./mcp 目录。
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
        # docker 的构建进度写在 stderr，PowerShell 5 在 ErrorActionPreference=Stop 下
        # 会把原生命令的 stderr 当异常抛出（NativeCommandError），即使构建本身成功也会中断脚本。
        # 这里临时把偏好设为 Continue，合流到 stdout，只按退出码判断成败。
        $prevEAP = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        & docker @buildArgs 2>&1 | ForEach-Object { Write-Host $_ }
        $dockerExit = $LASTEXITCODE
        $ErrorActionPreference = $prevEAP
        if ($dockerExit -ne 0) {
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
Write-Host "Windows 绿色版（仅网关本体）: $winZip" -ForegroundColor Green
Write-Host "插件与 MCP 扩展包: $extrasZip" -ForegroundColor Green
Write-Host "双击 $exe 即可启动并打开管理面板；需要插件或 MCP 时把扩展包解压到同一目录" -ForegroundColor Green
if ($Docker) {
    Write-Host "Docker 镜像（$($DockerPlatforms -join ' / ')）已导出：" -ForegroundColor Green
    foreach ($t in $tars) { Write-Host "  $t" -ForegroundColor Green }
    Write-Host "  目标机器导入: docker load -i <对应架构的 tar>" -ForegroundColor Green
    Write-Host "  镜像不含插件与 MCP：把扩展包解压到 compose 同级的 plugins/ 与 mcp/ 目录即可" -ForegroundColor Green
}