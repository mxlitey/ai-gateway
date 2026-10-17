#!/bin/sh
# 容器内验证：网关 + 三个 Python 插件（hello-py / trae / workbuddy）在 Linux 上的完整链路。
# 三个插件与 Windows / macOS 上跑的是同一份 .py 源码，容器里用系统 python3 启动。
# 用法：docker run --rm --entrypoint /app/verify.sh ai-gateway-test
#
# 基础镜像是 Alpine（busybox sh，无 curl），所有 HTTP 请求统一走 python3，
# 避免 busybox 与 GNU 工具的行为差异。
set -eu

BASE="http://127.0.0.1:${AIGW_PORT:-8787}"
ADMIN_PASSWORD="${AIGW_ADMIN_PASSWORD:-verify1234}"
LOG=/tmp/gateway.log

log() { echo "== $*"; }

fail() {
    echo "FAIL: $*" >&2
    echo "---- gateway log ----" >&2
    tail -n 80 "$LOG" >&2 || true
    exit 1
}

# ---- 通用 HTTP 调用：http METHOD PATH [BODY] [TOKEN] ----
# 输出一行 JSON：{"token":..., "body":..., "error":...}
# 第 4 个参数是裸 token，Authorization 头由本函数拼装。
http() {
    python3 - "$1" "${2:-}" "${3:-}" "${4:-}" "$BASE" <<'PY'
import json, sys, urllib.error, urllib.request

method, path, body, token, base = sys.argv[1:6]
data = body.encode() if body else None
req = urllib.request.Request(base + path, data=data, method=method)
if data:
    req.add_header("Content-Type", "application/json")
if token:
    req.add_header("Authorization", "Bearer " + token)

try:
    with urllib.request.urlopen(req, timeout=30) as resp:
        raw = resp.read().decode("utf-8", "replace")
except urllib.error.HTTPError as e:
    print(json.dumps({"error": e.read().decode("utf-8", "replace") or str(e.code)}, ensure_ascii=False))
    sys.exit(0)
except Exception as e:
    print(json.dumps({"error": str(e)}, ensure_ascii=False))
    sys.exit(0)

try:
    payload = json.loads(raw) if raw else None
except ValueError:
    payload = raw
token = payload.get("token") if isinstance(payload, dict) else None
print(json.dumps({"token": token, "body": payload}, ensure_ascii=False))
PY
}

# 从 http 输出里取字段：pick '<python 表达式，d 为解析后的响应>'
pick() {
    python3 -c '
import json, sys
d = json.loads(sys.stdin.read() or "{}")
if "error" in d:
    print("")
    sys.exit(0)
print(eval(sys.argv[1]))
' "$1"
}

# 直连插件的 GET（返回响应体前 200 字符）
probe() {
    python3 - "$1" <<'PY'
import sys, urllib.request
try:
    with urllib.request.urlopen(sys.argv[1], timeout=10) as resp:
        print(resp.read().decode("utf-8", "replace")[:200])
except Exception as e:
    print("ERR " + str(e))
PY
}

# ---- 1. 启动网关（首次启动用 --admin-password 完成初始化）----
gw_ready() {
    [ -n "$(http GET /health "" "" | pick 'd.get("body")')" ]
}

log "启动网关"
/app/ai-gateway --admin-password "$ADMIN_PASSWORD" --no-browser >"$LOG" 2>&1 &
GW_PID=$!
trap 'kill "$GW_PID" 2>/dev/null || true' EXIT

i=0
while ! gw_ready; do
    i=$((i + 1))
    if [ "$i" -gt 60 ]; then fail "网关 60 秒内未就绪"; fi
    sleep 1
done
log "网关就绪"

# ---- 2. 登录（数据卷可能已有旧密码，连续失败则清库重来）----
login() {
    http POST /admin/api/login "{\"password\":\"$ADMIN_PASSWORD\"}"
}

TOKEN=""
i=0
while [ -z "$TOKEN" ]; do
    TOKEN=$(login | pick 'd.get("token") or ""')
    [ -n "$TOKEN" ] && break
    i=$((i + 1))
    if [ "$i" -gt 3 ]; then
        log "默认密码不可用，清空数据目录后重启"
        kill -TERM "$GW_PID" 2>/dev/null || true
        wait "$GW_PID" 2>/dev/null || true
        rm -rf "${AIGW_DATA_DIR:-/app/data}"/*
        /app/ai-gateway --admin-password "$ADMIN_PASSWORD" --no-browser >"$LOG" 2>&1 &
        GW_PID=$!
        i=1
        sleep 2
    fi
    sleep 1
done
AUTH="$TOKEN"
log "登录成功"

# ---- 3. 等三个插件都进入 running ----
plugin_state() {
    # 输出 "<status>\t<last_error>"，插件不存在时为 missing。
    # 注意 /admin/api/plugins 返回的是裸数组，经 http() 包装后位于 body。
    http GET /admin/api/plugins "" "$AUTH" | python3 -c '
import json, sys
d = json.load(sys.stdin)
items = d.get("body") if isinstance(d.get("body"), list) else []
p = next((x for x in items if x.get("id") == sys.argv[1]), None)
if p is None:
    print("missing\t")
else:
    print((p.get("last_status") or "missing") + "\t" + (p.get("last_error") or "-"))
' "$1"
}

wait_plugin() {
    id=$1
    i=0
    while :; do
        line=$(plugin_state "$id")
        status=$(echo "$line" | cut -f1)
        err=$(echo "$line" | cut -f2)
        [ "$status" = "running" ] && { log "插件 $id: running"; return 0; }
        i=$((i + 1))
        if [ "$i" -gt 60 ]; then fail "插件 $id 状态=$status，last_error=$err"; fi
        sleep 1
    done
}

for p in hello-py trae workbuddy; do
    wait_plugin "$p"
done

# ---- 4. 逐个直连插件，确认协议可用 ----
log "hello-py /health: $(probe http://127.0.0.1:20801/health)"
log "hello-py /v1/models: $(probe http://127.0.0.1:20801/v1/models)"

CHAT=$(python3 - <<'PY'
import json, urllib.request
req = urllib.request.Request(
    "http://127.0.0.1:20801/v1/chat/completions",
    data=json.dumps({"model": "hello-py", "messages": [{"role": "user", "content": "docker"}]}).encode(),
    method="POST",
)
req.add_header("Content-Type", "application/json")
req.add_header("X-Plugin-Account", "acc-linux")
with urllib.request.urlopen(req, timeout=30) as resp:
    print(resp.read().decode())
PY
)
echo "$CHAT" | grep -q "acc-linux" || fail "Python 插件未按 X-Plugin-Account 选号：$CHAT"
log "hello-py chat: OK"

log "trae /health: $(probe http://127.0.0.1:20727/health)"
log "trae /console/accounts: $(probe http://127.0.0.1:20727/console/accounts)"
log "workbuddy /health: $(probe http://127.0.0.1:20800/health)"
log "workbuddy /console/accounts: $(probe http://127.0.0.1:20800/console/accounts)"

# ---- 5. 三个插件都应注册了渠道 ----
SOURCES=$(http GET /admin/api/channels "" "$AUTH" | python3 -c '
import json, sys
d = json.load(sys.stdin).get("body")
items = d if isinstance(d, list) else []
print(",".join(sorted(c["source"] for c in items if (c.get("source") or "").startswith("plugin:"))))
')
log "插件渠道: $SOURCES"
for want in plugin:hello-py plugin:trae plugin:workbuddy; do
    echo "$SOURCES" | grep -q "$want" || fail "缺少渠道 $want"
done

# ---- 6. 通过网关统一入口转发一次（覆盖代理层 → 插件渠道）----
# 插件渠道默认不带模型，先给 hello-py 渠道补一个模型，代理层即可路由。
python3 - "$BASE" "$TOKEN" <<'PY'
import json, sys, urllib.request

base, token = sys.argv[1], sys.argv[2]

def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("Authorization", "Bearer " + token)
    if data:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=20) as resp:
        return json.loads(resp.read() or b"{}")

channels = call("GET", "/admin/api/channels")
ch = next(c for c in channels if c.get("source") == "plugin:hello-py")
call("PUT", "/admin/api/channels/" + ch["id"], {"models": ["hello-py"]})
print("channel-model-ready")
PY

KEY=$(http POST /admin/api/apikeys '{"name":"verify"}' "$AUTH" |
    pick 'd.get("body",{}).get("key") or d.get("body",{}).get("secret") or d.get("body",{}).get("api_key") or ""')
[ -n "$KEY" ] || fail "未拿到客户端密钥"

PROXIED=$(python3 - "$BASE" "$KEY" <<'PY'
import json, sys, urllib.request
base, key = sys.argv[1], sys.argv[2]
req = urllib.request.Request(
    base + "/v1/chat/completions",
    data=json.dumps({"model": "hello-py", "messages": [{"role": "user", "content": "via-gateway"}]}).encode(),
    method="POST",
)
req.add_header("Authorization", "Bearer " + key)
req.add_header("Content-Type", "application/json")
with urllib.request.urlopen(req, timeout=30) as resp:
    print(resp.read().decode())
PY
)
echo "$PROXIED" | grep -q "via-gateway" || fail "网关转发未到达插件：$PROXIED"
log "网关 → 插件渠道转发: OK"

# ---- 7. 优雅退出：插件进程应随宿主回收 ----
log "停止网关并检查插件进程回收"
kill -TERM "$GW_PID"
wait "$GW_PID" 2>/dev/null || true
sleep 2
LEFT=$(ps -eo args | grep -E '/app/plugins/(trae|workbuddy)/|hello-py/main.py' | grep -v grep || true)
[ -z "$LEFT" ] || fail "网关退出后仍有插件进程存活：$LEFT"
log "插件进程已全部回收"

echo "ALL CHECKS PASSED"
