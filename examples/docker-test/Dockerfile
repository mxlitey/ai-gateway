# AI Gateway Linux 验证镜像（基于项目已有的 Alpine 运行镜像）
#
# 用途：在 Linux（容器）里跑完整的插件矩阵——三个插件都是同一份 Python 源码，
# 直接拷进镜像即可，不需要交叉编译：
#   hello-py   最小示例（标准库）
#   trae       TRAE 通道插件
#   workbuddy  WorkBuddy / CodeBuddy 插件
#
# 构建（仓库根目录，先执行 build/verify-docker.ps1 生成 out/ai-gateway）：
#   docker build -f examples/docker-test/Dockerfile -t ai-gateway-test .
# 运行：
#   docker run --rm --entrypoint /app/verify.sh ai-gateway-test
#
# 基础镜像用 alpine:3.20（与本项目 Dockerfile 的运行阶段一致），
# 只额外装 ca-certificates 与 python3——python3 就是插件的运行时。
FROM alpine:3.20

USER root

RUN apk add --no-cache ca-certificates python3 \
 && python3 --version

# 网关二进制由 build/verify-docker.ps1 交叉编译到 out/ 后拷入
COPY out/ai-gateway /app/ai-gateway

# 插件就是 Python 源码：整目录拷贝，与 Windows / macOS 上跑的是同一份文件
COPY examples/hello-py/ /app/plugins/hello-py/
COPY examples/trae/ /app/plugins/trae/
COPY examples/workbuddy/ /app/plugins/workbuddy/

# 注意：验证脚本不放进镜像，由 verify-docker.ps1 在运行时用 docker cp 注入。
# 这样改脚本不必重建镜像（重建会重新解析基础镜像，国内 registry 常失败）。
RUN chmod +x /app/ai-gateway \
 && mkdir -p /app/data \
 && rm -rf /app/plugins/*/__pycache__

ENV AIGW_HOST=0.0.0.0 \
    AIGW_PORT=8787 \
    AIGW_DATA_DIR=/app/data \
    AIGW_PLUGINS_DIR=/app/plugins

VOLUME /app/data
EXPOSE 8787

ENTRYPOINT ["/app/ai-gateway"]
