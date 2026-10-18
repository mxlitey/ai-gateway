# AI Gateway 多阶段构建：产出无界面（headless）的 Linux 镜像，供 Docker 部署。
#
# 构建：docker build -t ai-gateway:latest .
# 运行：docker run -d --name ai-gateway -p 8787:8787 \
#         -e AIGW_ADMIN_PASSWORD=your-password \
#         -v ai-gateway-data:/data \
#         ai-gateway:latest
#
# 说明：桌面外壳（cmd/gateway-wails，Wails + WebView2）仅面向 Windows，
# 带 //go:build windows 标签，不会参与本镜像构建。

# ---- 阶段 1：构建前端静态资源 ----
FROM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci 2>/dev/null || npm install
COPY web/ ./
RUN npm run build

# ---- 阶段 2：编译 Go 二进制（内嵌前端产物）----
FROM golang:1.26-alpine AS go-builder
WORKDIR /src
# 纯 Go 依赖（modernc sqlite），无需 CGO
ENV CGO_ENABLED=0 GOOS=linux
# 国内网络访问 proxy.golang.org 通常不通，默认走镜像；构建时可覆盖
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
ARG VERSION=dev
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
# 用阶段 1 的产物覆盖 web/dist，供 //go:embed 内嵌
COPY --from=web-builder /src/web/dist ./web/dist
RUN go build -trimpath -ldflags "-s -w -X github.com/czg95/ai-gateway-v2/internal/version.Version=${VERSION}" \
      -o /out/ai-gateway ./cmd/gateway

# ---- 阶段 3：精简运行时 ----
FROM alpine:3.20
# 插件一律是 Python 程序，宿主用 Python 解释器拉起入口脚本，镜像内必须有 python3；
# 另需基础 CA 证书与 tzdata 供上游 HTTPS / 本地时间使用
RUN apk add --no-cache ca-certificates tzdata python3 && \
    adduser -D -u 10001 gateway && \
    mkdir -p /data /plugins && \
    chown -R gateway:gateway /data /plugins
COPY --from=go-builder /out/ai-gateway /usr/local/bin/ai-gateway

# 数据与插件目录固定为卷挂载点：换个容器实例也能保留配置、密钥与插件
ENV AIGW_DATA_DIR=/data \
    AIGW_PLUGINS_DIR=/plugins \
    AIGW_HOST=0.0.0.0 \
    AIGW_PORT=8787
VOLUME ["/data", "/plugins"]

USER gateway
EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8787/health >/dev/null 2>&1 || exit 1

ENTRYPOINT ["ai-gateway"]
