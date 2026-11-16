"""qoder_fingerprint.py —— 统一设备指纹稳定派生模块 (derive_id)

无论是国内版 (qoder.com.cn / gateway.qoder.com.cn) 还是国际版
(qoder.com / api1.qoder.sh，官方候选 api1→api2→api3)，均通过本模块基于账号
UID 和加盐哈希单向派生
固定的伪物理设备特征 (machineId / sessionId)，确保每个账号长期来自同一台
虚拟物理设备，且多账号之间天然隔离，阻断跨账号关联风控。

COSY 推理签名会携带 cosy-machineid / cosy-machinetoken / cosy-machinetoken，
这里提供与 WorkBuddy 网关同构的稳定派生逻辑：
  - machineId  : 由 uid 稳定派生（同一账号永远相同）
  - sessionId  : 由 uid 稳定派生（会话隔离）
  - request id : 每请求纯随机（与账号无关，见 generate_request_id）

形态口径（2026-10-20 修订）：官方客户端的 machineId 取自 OS 硬件 UUID，是
**带连字符的 UUIDv4 形状**；请求 id 则是**每请求独立的随机 32 位 hex**。本模块
此前输出 32 位无连字符 hex，请求 id 还带 uid 派生的固定前缀——两者都对不上官方
形态，本身就是可被聚类的「网关特征」。现按官方形态对齐（与 WorkBuddy 网关
v5.8.4 抓包后的修正同口径）。

取值范围仍必须按账号派生：官方客户端「一个进程 = 一台设备」，而网关是一个长驻
进程同时冒充几十台设备。照官方那样在进程维度取一次，等于让所有账号共用同一个
值，反而把它们关联到同一台机器上，与「每账号一台设备」的隔离口径自相矛盾。
"""
import hashlib
import os
import time
import uuid


def _device_id_legacy() -> bool:
    """QD_DEVICE_ID_LEGACY=1 时保留旧的「32 位无连字符 hex」形态。

    issue #61 把设备码改成 UUID 形状对齐官方客户端，代价是**存量账号的
    machineId/sessionId 会变**（对上游等于换了一台设备）。已经稳定跑着的号池
    如果想避免这次「换设备」，升级前设这个开关即可保持旧值不变。
    新号池不需要它（默认 UUID 形状更接近官方）。
    """
    raw = str(os.environ.get("QD_DEVICE_ID_LEGACY") or "").strip().lower()
    return raw in ("1", "on", "true", "yes", "enable", "enabled")


def derive_id(uid: str, salt: str) -> str:
    """由 uid + salt 稳定派生一个**带连字符的 UUID 形状**设备/会话标识。

    幂等：同一账号每次调用产生相同值，彻底避免随机机器码导致的上游风控。
    输出形如 `a1b2c3d4-e5f6-4a78-a9b0-c1d2e3f4a5b6`——版本位固定为 `4`、
    变体位固定为 `a`，与真实客户端的 machineId 逐字符同形。

    ⚠️ 破坏性变更提示：本函数输出形态在 2026-10-20 由「32 位无连字符 hex」
    改为「UUID 带连字符」，因此**已入池账号的 machineId / sessionId 会变**。
    对上游而言等于这些账号换了一台设备。新号池无影响；存量号池升级前请评估
    （官方对「换设备」本身不敏感，真正敏感的是同设备关联多号）。
    """
    seed = f"{salt}:{uid or 'anonymous'}"
    h = hashlib.md5(seed.encode("utf-8")).hexdigest()
    if _device_id_legacy():
        return h            # 旧形态：32 位无连字符 hex（存量号池平滑用）
    # 伪造 UUIDv4 的版本位（第 13 位）与变体位（第 17 位），与官方同形。
    h = h[:12] + "4" + h[13:16] + "a" + h[17:]
    return "%s-%s-%s-%s-%s" % (h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])


def generate_request_id(uid: str) -> str:
    """生成 X-Request-ID：32 位无连字符 hex，**每请求纯随机**。

    真实客户端的请求 id 与账号无关（每条随机），所以这里**不再掺 uid 派生的
    前缀**——那是一条会出现在每个请求里的稳定「网关特征」，最容易被动聚类。

    uid 参数保留只为兼容既有调用点（签名不破坏）。
    """
    return uuid.uuid4().hex


def derive_machine_type(uid: str) -> str:
    """稳定派生 18 位去横线的 machine_type（cosy-machinetoken 同形）。"""
    return derive_id(uid, "machinetype").replace("-", "")[:18]


def derive_machine_token(uid: str) -> str:
    """稳定派生 machine_token（base64url 风格的随机串外观）。"""
    raw = hashlib.sha512(f"machinetoken:{uid}".encode("utf-8")).digest()
    import base64
    return base64.urlsafe_b64encode(raw).decode("ascii").rstrip("=")[:43]


# ---------------------------------------------------------------------------
# 本机虚拟化状态检测（中文输出）
# ---------------------------------------------------------------------------
# 用途：官方风控（runtime-info.exe）会返回 vmInfo（是否虚拟机 / 平台 / 评分），
# 活动和风控策略可能据此区别对待；看板与 _diag_campaign.py 都展示这份结果。
# 判定来源：
#   1) 官方风控桥 runtime-info.exe 的 vmInfo（最权威：官方客户端就是这么判的）
#   2) 本机交叉校验（CPU 型号字符串 / 系统制造商 / 常见虚拟化驱动文件）
VM_BRANDS_CN = {
    "hyper-v": "Hyper-V（微软）",
    "vmware": "VMware",
    "virtualbox": "Oracle VirtualBox",
    "vbox": "Oracle VirtualBox",
    "kvm": "KVM",
    "qemu": "QEMU",
    "xen": "Xen",
    "parallels": "Parallels",
    "virtual machine": "通用虚拟机",
    "bochs": "Bochs",
    "bhyve": "bhyve",
}

# 常见虚拟机/半虚拟化驱动（存在任一即强烈提示运行在虚拟机内）
_VM_DRIVER_FILES = (
    "vmbus.sys",          # Hyper-V
    "vmci.sys",           # VMware
    "vmhgfs.sys",         # VMware 共享目录
    "vboxguest.sys",      # VirtualBox
    "vboxmouse.sys",      # VirtualBox
    "vmmouse.sys",        # VMware
    "virtio_balloon.sys", # KVM/QEMU
    "viostor.sys",        # KVM/QEMU 存储
    "xenevtchn.sys",      # Xen
)



_SBC_MARKERS = ("raspberry pi", "banana pi", "orange pi", "rock pi", "tinker board",
                "odroid", "beaglebone", "nanopi", "radxa", "khadas")


def _linux_sbc_model():
    """读设备树 model，判断是不是单板计算机（issue #123）。

    树莓派这类 SBC 在 ARM 上会暴露一些"像虚拟机"的特征（无 hypervisor 的
    精简 /proc/cpuinfo、systemd-detect-virt 可能报 docker/none），此前的启发式
    会把它们误判成 KVM。设备树里的型号名是权威且便宜的判据。
    """
    for path in ("/proc/device-tree/model",
                 "/sys/firmware/devicetree/base/model"):
        try:
            with open(path, "rb") as fh:
                text = fh.read(200).decode("utf-8", "replace").strip("\x00 \n")
        except Exception:
            continue
        if not text:
            continue
        low = text.lower()
        for mark in _SBC_MARKERS:
            if mark in low:
                return text
    return ""


def vm_brand_cn(brand: str) -> str:
    """虚拟化平台中文名（未知品牌原样返回）。"""
    b = str(brand or "").strip()
    if not b:
        return ""
    low = b.lower()
    for key, cn in VM_BRANDS_CN.items():
        if key in low:
            return cn
    return b


def _vm_level_cn(score):
    """风控评分 -> 中文档位。"""
    try:
        s = int(score)
    except (TypeError, ValueError):
        return "未知"
    if s >= 70:
        return "高"
    if s >= 40:
        return "中"
    if s > 0:
        return "低"
    return "无"


def _local_vm_evidence():
    """本机交叉校验：返回 (evidence 列表, 品牌提示 或 '')。全部中文描述。"""
    evidence, hint = [], ""
    # 0) 单板计算机（树莓派等）：设备树型号是权威判据，命中就不再往下猜
    #    —— issue #123：树莓派 3B+ 曾被误判成 KVM 虚拟机。
    _sbc = _linux_sbc_model()   # 非 Linux 上设备树不存在，函数自身返回空
    if _sbc:
        return ["检测到单板计算机：%s" % _sbc], ""
    # 1) CPU 型号字符串（注册表 / /proc/cpuinfo）
    cpu = ""
    if os.name == "nt":
        try:
            import winreg
            with winreg.OpenKey(
                    winreg.HKEY_LOCAL_MACHINE,
                    r"HARDWARE\DESCRIPTION\System\CentralProcessor\0") as k:
                cpu = str(winreg.QueryValueEx(k, "ProcessorNameString")[0])
            with winreg.OpenKey(
                    winreg.HKEY_LOCAL_MACHINE,
                    r"HARDWARE\DESCRIPTION\System\BIOS") as k:
                maker = str(winreg.QueryValueEx(k, "SystemManufacturer")[0])
                model = str(winreg.QueryValueEx(k, "SystemProductName")[0])
            if maker or model:
                evidence.append("系统制造商/型号：%s %s" % (maker, model))
                for token in ("VMware", "VirtualBox", "Virtual Machine",
                              "KVM", "QEMU", "Xen", "Parallels", "Hyper-V"):
                    if token.lower() in (maker + " " + model).lower():
                        hint = hint or token
                        break
        except Exception:
            pass
    else:
        try:
            with open("/proc/cpuinfo", encoding="utf-8", errors="replace") as fh:
                text = fh.read()
            for line in text.splitlines():
                if line.lower().startswith("model name"):
                    cpu = line.split(":", 1)[1].strip()
                    break
            if "hypervisor" in text.lower():
                evidence.append("CPU 标志含 hypervisor（运行在虚拟机监控程序之上）")
        except Exception:
            pass
    if cpu:
        evidence.append("处理器型号：%s" % cpu)
        for token in ("Virtual CPU", "VirtualBox", "VMware", "QEMU",
                      "KVM", "Xeon Platinum 8", "Virtual Machine"):
            if token.lower() in cpu.lower():
                hint = hint or token
                break
    # 2) 常见虚拟化驱动文件
    if os.name == "nt":
        drv = os.path.join(os.environ.get("SystemRoot") or r"C:\Windows",
                           "System32", "drivers")
        found = [f for f in _VM_DRIVER_FILES
                 if os.path.isfile(os.path.join(drv, f))]
        if found:
            evidence.append("检测到虚拟化驱动：%s" % ", ".join(sorted(found)))
            if not hint:
                low = " ".join(found).lower()
                hint = ("Hyper-V" if "vmbus" in low else
                        "VMware" if "vmci" in low or "vmhgfs" in low or "vmmouse" in low else
                        "VirtualBox" if "vbox" in low else
                        "KVM/QEMU" if "virtio" in low or "viostor" in low else
                        "Xen" if "xen" in low else "")
    return evidence, hint


def vm_status(bridge_vm_info=None, bridge_available=None):
    """本机虚拟化状态（中文输出），供看板 / 体检脚本共用。

    bridge_vm_info：官方风控桥 runtime-info.exe 返回的 vmInfo（最权威）
    bridge_available：官方风控桥是否可用（None=未知）。它不改变 source（source
                      只看 vmInfo 里有没有 isVm），只用于把 summary 里的降级
                      原因说对：桥不可用 vs 桥可用但没给 vmInfo。

    返回：
      {is_vm, level(高/中/低/无/未知), score, brand, brand_cn,
       vm_type_code, source(runtime-info/local), evidence[], summary}
    """
    evidence, hint = _local_vm_evidence()
    info = bridge_vm_info if isinstance(bridge_vm_info, dict) else {}
    is_vm = info.get("isVm")
    score = info.get("percentage")
    brand = str(info.get("brand") or "") or hint
    if is_vm is None:
        # 桥不可用/无 isVm 字段：结论全部来自本机交叉校验，来源只能是 local。
        # 曾经的判据 `evidence or bridge_available is None` 在「桥不可用 +
        # 物理机」下双双为假，把本机交叉校验谎报成 runtime-info —— 看板因此
        # 少一句「官方风控桥不可用」的降级说明（dashboard 只在 source==local
        # 时显示它），用户看到一个没有证据支撑的来源。
        is_vm = bool(hint) or any("虚拟化驱动" in e or "hypervisor" in e
                                 for e in evidence)
        source = "local"
    else:
        source = "runtime-info"
    if info:
        evidence.insert(0, "官方风控判定：%s（评分 %s，类型码 %s）"
                        % ("是虚拟机" if is_vm else "不是虚拟机",
                           score if score is not None else "-",
                           info.get("vmTypeCode") if info.get("vmTypeCode") is not None else "-"))
    brand_cn = vm_brand_cn(brand)
    if is_vm and brand_cn:
        summary = "本机运行在虚拟机中（%s，风控评分 %s/100）" % (brand_cn, score if score is not None else "-")
    elif is_vm:
        summary = "本机疑似运行在虚拟机中（未识别虚拟化平台）"
    else:
        summary = "未检测到虚拟化运行环境（物理机）"
    if source == "local":
        # “本机交叉校验”这个来源不变，但**原因**必须说对：桥不可用 vs
        # 桥能用却没给出 vmInfo，是两回事。写成一句“桥不可用”会在后者
        # 场景下与同一响应里的 bridge_available=true 自相矛盾（看板据此显示
        # 的降级说明也是假的）。
        if bridge_available is False:
            summary += "；官方风控桥不可用，以上为本机交叉校验结果"
        elif bridge_available:
            summary += "；官方风控桥未返回虚拟机信息，以上为本机交叉校验结果"
        else:
            summary += "；以上为本机交叉校验结果"
    return {
        "is_vm": bool(is_vm),
        "level": _vm_level_cn(score) if is_vm else "无",
        "score": score if score is not None else None,
        "brand": brand,
        "brand_cn": brand_cn,
        "vm_type_code": info.get("vmTypeCode"),
        "source": source,
        "evidence": evidence,
        "summary": summary,
    }


def get_desktop_fingerprint(uid: str, nickname: str = "", os_name: str = "win32") -> dict:
    """生成上报事件所需的标准完整桌面端指纹（行为事件上报用）。"""
    now = int(time.time() * 1000)
    return {
        "timezone": "Asia/Shanghai",
        "reportDelay": 2000,
        "userId": uid,
        "username": nickname,
        "userNickname": nickname,
        "product": "SaaS",
        "releaseDate": 1789036585355,
        "commit": "5f9692923c93033111c51ad7b003eb80204a9b75",
        "ideName": "Qoder",
        "ideType": "Qoder",
        "ideVersion": "1.1.64",
        "machineId": derive_id(uid, "machine"),
        "sessionId": derive_id(uid, "session"),
        "extName": "qoder-desktop",
        "extVersion": "1.1.64",
        "os": os_name,
        "arch": "x64",
        "osVersion": "10.0.26220",
        "cpuCores": 20,
        "memorySize": 24,
        "timestamp": now,
        "presentAt": now,
    }
