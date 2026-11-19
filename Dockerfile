# AI Gateway 多阶段构建：产出无界面（headless）的 Linux 镜像，供 Docker 部署。
#
# 构建（单架构）：docker buildx build --platform linux/amd64 -t ai-gateway:v<版本号>-amd64 --load .
# 双架构（amd64 + arm64，需 buildx 与 QEMU）：
#   docker buildx build --platform linux/amd64,linux/arm64 -t ai-gateway:v<版本号> .
# 版本号默认读取仓库根目录的 VERSION 文件，也可用 --build-arg VERSION=x.y.z 覆盖。
# 运行：docker run -d --name ai-gateway -p 8787:8787 \
#         -e AIGW_ADMIN_PASSWORD=your-password \
#         -v ai-gateway-data:/data \
#         ai-gateway:v<版本号>
#
# 镜像只含网关本体：插件与本地 MCP 由用户自行放进映射目录（/plugins、/mcp），
# 启动时自动扫描。要预置内容，把对应目录挂进来即可（见 docker-compose.yml 的注释）。
# 说明：桌面外壳（cmd/gateway-wails，Wails + WebView2）仅面向 Windows，
# 带 //go:build windows 标签，不会参与本镜像构建。

# ---- 阶段 1：构建前端静态资源（在构建机架构上跑，arm64 时不走 QEMU）----
FROM --platform=$BUILDPLATFORM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci 2>/dev/null || npm install
COPY web/ ./
RUN npm run build

# ---- 阶段 2：编译 Go 二进制（内嵌前端产物）----
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS go-builder
WORKDIR /src
# 纯 Go 依赖（modernc sqlite），无需 CGO；用构建机架构编译，靠 GOARCH 交叉出目标架构二进制
ENV CGO_ENABLED=0 GOOS=linux
ARG TARGETARCH=amd64
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
ARG VERSION=
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
# 用阶段 1 的产物覆盖 web/dist，供 //go:embed 内嵌
COPY --from=web-builder /src/web/dist ./web/dist
RUN VERSION="${VERSION:-$(cat VERSION)}" && \
    GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X github.com/czg95/ai-gateway-v2/internal/version.Version=${VERSION}" \
      -o /out/ai-gateway ./cmd/gateway

# ---- 阶段 3：精简运行时 ----
# python:3.11-slim 已自带 python3 / pip（多架构镜像直接拉取），
# 省掉在 QEMU 下 apt 安装 python3-pip 的数分钟开销。
FROM python:3.11-slim-bookworm
# 插件一律是 Python 程序，宿主用 Python 解释器拉起入口脚本，镜像内必须有 python3；
# pip 用于安装插件在 plugin.json 里声明的 requirements；
# 另需 tzdata 供本地时间使用（CA 证书该基础镜像已包含）。
#
# deb.debian.org 在国内经常连不上（构建会卡几分钟后失败），默认换成国内镜像；
# 需要官方源或别的镜像时用 --build-arg APT_MIRROR=deb.debian.org 覆盖，留空则不改。
ARG APT_MIRROR=mirrors.aliyun.com
RUN set -eux; \
    if [ -n "$APT_MIRROR" ]; then \
        if [ -f /etc/apt/sources.list.d/debian.sources ]; then \
            sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources; \
        else \
            sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list; \
        fi; \
    fi; \
    apt-get -o Acquire::Retries=3 update; \
    apt-get install -y --no-install-recommends tzdata; \
    rm -rf /var/lib/apt/lists/*; \
    useradd --create-home --uid 10001 gateway; \
    mkdir -p /data /plugins /mcp; \
    chown -R gateway:gateway /data /plugins /mcp
COPY --from=go-builder /out/ai-gateway /usr/local/bin/ai-gateway

# 数据与插件目录固定为卷挂载点：换个容器实例也能保留配置、密钥与插件。
# /plugins 与 /mcp 只建空目录，内容由用户挂载（镜像不自带插件与 MCP）。
ENV AIGW_DATA_DIR=/data \
    AIGW_PLUGINS_DIR=/plugins \
    AIGW_MCP_DIR=/mcp \
    AIGW_HOST=0.0.0.0 \
    AIGW_PORT=8787
VOLUME ["/data", "/plugins", "/mcp"]

USER gateway
EXPOSE 8787

# debian-slim 自带 busybox，没有 wget/curl，用 python3 探活（镜像内已有解释器）
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD python3 -c "import urllib.request,sys;sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8787/health',timeout=4).status==200 else 1)"

ENTRYPOINT ["ai-gateway"]
