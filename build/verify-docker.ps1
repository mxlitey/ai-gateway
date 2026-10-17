# 一键构建并运行 Docker 验证：网关 + 三个 Python 插件（hello-py / trae / workbuddy）
#
# 用法（仓库根目录）：
#   pwsh -File build/verify-docker.ps1
param(
    [string]$Image = "ai-gateway-test",
    [switch]$KeepImage
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

# go build 失败不会抛异常（只写 $LASTEXITCODE），必须显式检查，否则会带着空产物去构建镜像
function Invoke-Go([string]$desc, [string[]]$arguments) {
    & go @arguments
    if ($LASTEXITCODE -ne 0) {
        throw "编译失败：$desc"
    }
}

$out = Join-Path $root "out"
Remove-Item -Recurse -Force $out -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $out | Out-Null

$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
$env:GOARCH = "amd64"
if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn,direct" }

Write-Host "[1/3] 交叉编译 Linux 产物 ..." -ForegroundColor Cyan
# 只编译网关自身：插件是 Python 源码，同一份文件直接拷进镜像，不需要交叉编译。
# 桌面入口 cmd/gateway-wails 依赖 Wails + WebView2，只能在 Windows 构建；
# Linux / Docker 用命令行入口 cmd/gateway（后端 + 浏览器面板）。
Invoke-Go "ai-gateway (linux)" @("build", "-trimpath", "-ldflags", "-s -w", "-o", (Join-Path $out "ai-gateway"), "./cmd/gateway")

Write-Host "[2/3] 构建镜像 $Image ..." -ForegroundColor Cyan
# 基础镜像是 alpine:3.20（公开镜像，可拉取）；脚本不进镜像，改脚本无需重建。
docker build -f examples/docker-test/Dockerfile -t $Image .

Write-Host "[3/3] 容器内验证 ..." -ForegroundColor Cyan
# 验证脚本由 docker cp 注入（改脚本不必重建镜像）。
# 容器只作为 Linux 环境：网关与插件都由脚本自己拉起，这样能验证进程回收。
$container = "aigw-verify-$([Guid]::NewGuid().ToString('N').Substring(0, 8))"
docker run --rm -d --name $container --entrypoint sh $Image -c "sleep 1800" | Out-Null
try {
    docker cp "examples/docker-test/verify-container.sh" "${container}:/app/verify.sh"
    docker exec $container chmod +x /app/verify.sh
    docker exec -e AIGW_ADMIN_PASSWORD=verify1234 -e AIGW_PORT=8787 $container /app/verify.sh
    $code = $LASTEXITCODE
} finally {
    docker rm -f $container | Out-Null
}

if (-not $KeepImage) {
    docker rmi $Image --force | Out-Null
    Remove-Item -Recurse -Force $out -ErrorAction SilentlyContinue
}
exit $code
