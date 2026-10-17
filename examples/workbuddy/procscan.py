# -*- coding: utf-8 -*-
"""从正在运行的桌面端进程内存里找回 protector key（Windows 专属）。

背景：atRestSecretKey 编译在客户端原生模块里，磁盘上没有明文，只存在于运行中的进程内存。
判据：sha256(sha256(候选字符串))[:16] 等于 keyblob 里 static-v1 槽的 protectorKeyId，
因此候选不需要理解上下文，命中即可信。

非 Windows 平台直接给出与 Go 版逐字一致的提示文案。
"""

import base64
import hashlib
import os
import sys

WINDOWS_ONLY_MSG = "读取本机桌面端登录态目前只支持 Windows"

MEM_COMMIT = 0x1000
MEM_PRIVATE = 0x20000

PAGE_NOACCESS = 0x01
PAGE_READONLY = 0x02
PAGE_READWRITE = 0x04
PAGE_WRITECOPY = 0x08
PAGE_EXECUTE_READ = 0x20
PAGE_EXECUTE_READWRITE = 0x40
PAGE_EXECUTE_WRITECOPY = 0x80
PAGE_GUARD = 0x100

SCAN_CHUNK = 4 << 20
SCAN_OVERLAP = 128

PROCESS_VM_READ = 0x0010
PROCESS_QUERY_INFORMATION = 0x0400

TH32CS_SNAPPROCESS = 0x00000002

# 桌面端可执行文件名（插件自己的脚本不叫这些名字，但桌面端可能就叫 workbuddy.exe）
DESKTOP_EXE_NAMES = ("workbuddy.exe", "workbuddyai.exe", "codebuddy.exe")


def is_windows():
    return sys.platform.startswith("win")


# ---------- 候选校验（跨平台，纯计算） ----------


def _is_base64_char(b):
    return (0x41 <= b <= 0x5A) or (0x61 <= b <= 0x7A) or (0x30 <= b <= 0x39) or b in (0x2B, 0x2F)


def _check_candidate(cand, want):
    """校验候选：两层派生 sha256，命中的候选本身作为解密用的 key。"""
    key = hashlib.sha256(cand).digest()
    fingerprint = hashlib.sha256(key).hexdigest()[:16]
    if fingerprint != want:
        return None
    # 附加约束：候选必须是 32 字节的规范 base64（与客户端 isCanonical32ByteBase64 一致）
    try:
        raw = base64.b64decode(cand, validate=True)
    except Exception:
        return None
    if len(raw) != 32:
        return None
    return key


def scan_compact(buf, want):
    """找 43 个 base64 字符 + '='（共 44 字符）的候选。"""
    n = len(buf)
    i = 0
    while i + 44 <= n:
        if buf[i + 43] != 0x3D or not _is_base64_char(buf[i]):
            i += 1
            continue
        if i > 0 and _is_base64_char(buf[i - 1]):
            i += 1
            continue
        if i + 44 < n and (_is_base64_char(buf[i + 44]) or buf[i + 44] == 0x3D):
            i += 1
            continue
        ok = True
        for j in range(i, i + 43):
            if not _is_base64_char(buf[j]):
                ok = False
                break
        if not ok:
            i += 1
            continue
        key = _check_candidate(buf[i:i + 44], want)
        if key is not None:
            return key
        i += 44
    return None


def scan_utf16(buf, want):
    """找 UTF-16LE 排布的同一个候选（JS 字符串是 UTF-16）。"""
    n = len(buf)
    i = 0
    while i + 88 <= n:
        if buf[i + 1] != 0 or buf[i + 87] != 0 or buf[i + 86] != 0x3D or not _is_base64_char(buf[i]):
            i += 2
            continue
        if i >= 2 and buf[i - 1] == 0 and _is_base64_char(buf[i - 2]):
            i += 2
            continue
        if i + 89 < n and buf[i + 88] != 0 and _is_base64_char(buf[i + 88]):
            i += 2
            continue
        ok = True
        for j in range(43):
            if buf[i + 2 * j + 1] != 0 or not _is_base64_char(buf[i + 2 * j]):
                ok = False
                break
        if not ok:
            i += 2
            continue
        cand = bytearray(44)
        for j in range(43):
            cand[j] = buf[i + 2 * j]
        cand[43] = 0x3D
        key = _check_candidate(bytes(cand), want)
        if key is not None:
            return key
        i += 2
    return None


def scan_windows(buf, want, stride=8):
    """按栅格找「已经在内存里的派生 key 本身」。"""
    i = 0
    while i + 32 <= len(buf):
        window = buf[i:i + 32]
        if hashlib.sha256(window).hexdigest()[:16] == want:
            return bytes(window)
        i += stride
    return None


def scan_buffer(buf, want):
    """在一个内存块里找候选密钥：紧凑 → UTF-16 → 派生 key 栅格。"""
    key = scan_compact(buf, want)
    if key is not None:
        return key
    key = scan_utf16(buf, want)
    if key is not None:
        return key
    return scan_windows(buf, want, 8)


# ---------- Windows 内存扫描 ----------

if is_windows():
    import ctypes
    from ctypes import wintypes

    _kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)

    class _MemoryBasicInformation(ctypes.Structure):
        """MEMORY_BASIC_INFORMATION（64 位布局，48 字节）。"""

        _fields_ = [
            ("BaseAddress", ctypes.c_void_p),
            ("AllocationBase", ctypes.c_void_p),
            ("AllocationProtect", wintypes.DWORD),
            ("PartitionId", wintypes.WORD),
            ("Alignment1", wintypes.WORD),
            ("RegionSize", ctypes.c_size_t),
            ("State", wintypes.DWORD),
            ("Protect", wintypes.DWORD),
            ("Type", wintypes.DWORD),
            ("Alignment2", wintypes.DWORD),
        ]

    class _ProcessEntry32(ctypes.Structure):
        _fields_ = [
            ("dwSize", wintypes.DWORD),
            ("cntUsage", wintypes.DWORD),
            ("th32ProcessID", wintypes.DWORD),
            ("th32DefaultHeapID", ctypes.POINTER(ctypes.c_ulong)),
            ("th32ModuleID", wintypes.DWORD),
            ("cntThreads", wintypes.DWORD),
            ("th32ParentProcessID", wintypes.DWORD),
            ("pcPriClassBase", ctypes.c_long),
            ("dwFlags", wintypes.DWORD),
            ("szExeFile", ctypes.c_char * 260),
        ]

    _kernel32.VirtualQueryEx.argtypes = [
        wintypes.HANDLE, ctypes.c_void_p,
        ctypes.POINTER(_MemoryBasicInformation), ctypes.c_size_t,
    ]
    _kernel32.VirtualQueryEx.restype = ctypes.c_size_t
    _kernel32.ReadProcessMemory.argtypes = [
        wintypes.HANDLE, ctypes.c_void_p, ctypes.c_void_p, ctypes.c_size_t,
        ctypes.POINTER(ctypes.c_size_t),
    ]
    _kernel32.ReadProcessMemory.restype = wintypes.BOOL
    _kernel32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    _kernel32.OpenProcess.restype = wintypes.HANDLE
    _kernel32.CloseHandle.argtypes = [wintypes.HANDLE]
    _kernel32.CloseHandle.restype = wintypes.BOOL
    _kernel32.CreateToolhelp32Snapshot.argtypes = [wintypes.DWORD, wintypes.DWORD]
    _kernel32.CreateToolhelp32Snapshot.restype = wintypes.HANDLE
    _kernel32.Process32First.argtypes = [wintypes.HANDLE, ctypes.POINTER(_ProcessEntry32)]
    _kernel32.Process32First.restype = wintypes.BOOL
    _kernel32.Process32Next.argtypes = [wintypes.HANDLE, ctypes.POINTER(_ProcessEntry32)]
    _kernel32.Process32Next.restype = wintypes.BOOL


def _desktop_process_ids():
    """枚举正在运行的桌面端进程（排除自己）。"""
    snapshot = _kernel32.CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0)
    if snapshot == wintypes.HANDLE(-1).value or not snapshot:
        raise RuntimeError("枚举进程失败")
    try:
        out = []
        self_pid = os.getpid()
        entry = _ProcessEntry32()
        entry.dwSize = ctypes.sizeof(_ProcessEntry32)
        if not _kernel32.Process32First(snapshot, ctypes.byref(entry)):
            return out
        while True:
            name = entry.szExeFile.decode("mbcs", "replace").lower()
            if entry.th32ProcessID != self_pid and name in DESKTOP_EXE_NAMES:
                out.append(entry.th32ProcessID)
            if not _kernel32.Process32Next(snapshot, ctypes.byref(entry)):
                break
        return out
    finally:
        _kernel32.CloseHandle(snapshot)


def _readable_protect(protect):
    if protect & PAGE_GUARD or protect & PAGE_NOACCESS:
        return False
    return (protect & 0xFF) in (
        PAGE_READONLY, PAGE_READWRITE, PAGE_WRITECOPY,
        PAGE_EXECUTE_READ, PAGE_EXECUTE_READWRITE, PAGE_EXECUTE_WRITECOPY,
    )


def _scan_region(handle, base, size, want):
    buf = ctypes.create_string_buffer(SCAN_CHUNK)
    offset = 0
    while offset < size:
        n = min(SCAN_CHUNK, size - offset)
        read = ctypes.c_size_t(0)
        ok = _kernel32.ReadProcessMemory(
            handle, ctypes.c_void_p(base + offset), buf, n, ctypes.byref(read))
        if ok and read.value > 0:
            key = scan_buffer(buf.raw[:read.value], want)
            if key is not None:
                return key
        if read.value <= SCAN_OVERLAP:
            break
        offset += read.value - SCAN_OVERLAP
    return None


def _scan_process(handle, want):
    """两遍扫描：先只扫可写私有内存（JS 堆在这里，最快），全都没中再放宽。"""
    for only_private in (True, False):
        addr = 0
        while True:
            mbi = _MemoryBasicInformation()
            ret = _kernel32.VirtualQueryEx(handle, ctypes.c_void_p(addr), ctypes.byref(mbi),
                                           ctypes.sizeof(mbi))
            if not ret:
                break
            next_addr = (mbi.BaseAddress or 0) + mbi.RegionSize
            if (mbi.State == MEM_COMMIT and mbi.RegionSize > 0
                    and _readable_protect(mbi.Protect)
                    and (not only_private or mbi.Type == MEM_PRIVATE)):
                key = _scan_region(handle, mbi.BaseAddress or 0, mbi.RegionSize, want)
                if key is not None:
                    return key
            if next_addr <= addr:
                break
            addr = next_addr
    return None


def find_protector_key(protector_key_id):
    """返回 protector key（32 字节）；非 Windows 或找不到时抛 RuntimeError。"""
    if not is_windows():
        raise RuntimeError(WINDOWS_ONLY_MSG)

    want = (protector_key_id or "").strip().lower()
    if not want:
        raise RuntimeError("keyblob 里没有 protectorKeyId")

    pids = _desktop_process_ids()
    if not pids:
        raise RuntimeError(
            "没找到正在运行的桌面端进程：密钥只存在于进程内存，请先打开 WorkBuddy 并保持登录")

    denied = 0
    for pid in pids:
        handle = _kernel32.OpenProcess(PROCESS_VM_READ | PROCESS_QUERY_INFORMATION, False, pid)
        if not handle:
            denied += 1
            continue
        try:
            key = _scan_process(handle, want)
        finally:
            _kernel32.CloseHandle(handle)
        if key is not None:
            return key

    if denied == len(pids):
        raise RuntimeError(
            "无法读取桌面端进程内存（权限不足）：请以管理员身份运行网关，或关闭桌面端的额外保护后重试")
    raise RuntimeError(
        "已扫描 %d 个桌面端进程但没有找回密钥：请先打开 WorkBuddy 桌面端界面（让它解密一次登录态），"
        "稍等几秒再试；也可以改用 OAuth 授权登录" % len(pids))


def probe_processes(protector_key_id):
    """诊断：统计各进程的可读区域与候选分布，用于排查「扫不到密钥」。"""
    if not is_windows():
        raise RuntimeError(WINDOWS_ONLY_MSG)

    want = (protector_key_id or "").strip().lower()
    lines = []
    for pid in _desktop_process_ids():
        handle = _kernel32.OpenProcess(PROCESS_VM_READ | PROCESS_QUERY_INFORMATION, False, pid)
        if not handle:
            lines.append("pid=%d 打不开进程" % pid)
            continue
        try:
            stats = _probe_process(handle, want)
        finally:
            _kernel32.CloseHandle(handle)
        lines.append("pid=%d 区域=%d 读取=%.1fMB 紧凑候选=%d UTF16候选=%d" % (
            pid, stats["regions"], stats["read"] / (1 << 20),
            stats["compact"], stats["utf16"]))
    return lines


def _probe_process(handle, want):
    stats = {"regions": 0, "read": 0.0, "compact": 0, "utf16": 0}
    addr = 0
    while True:
        mbi = _MemoryBasicInformation()
        ret = _kernel32.VirtualQueryEx(handle, ctypes.c_void_p(addr), ctypes.byref(mbi),
                                       ctypes.sizeof(mbi))
        if not ret:
            break
        next_addr = (mbi.BaseAddress or 0) + mbi.RegionSize
        if mbi.State == MEM_COMMIT and mbi.RegionSize > 0 and _readable_protect(mbi.Protect):
            stats["regions"] += 1
            _probe_region(handle, mbi.BaseAddress or 0, mbi.RegionSize, stats)
        if next_addr <= addr:
            break
        addr = next_addr
    return stats


def _probe_region(handle, base, size, stats):
    buf = ctypes.create_string_buffer(SCAN_CHUNK)
    offset = 0
    while offset < size:
        n = min(SCAN_CHUNK, size - offset)
        read = ctypes.c_size_t(0)
        ok = _kernel32.ReadProcessMemory(
            handle, ctypes.c_void_p(base + offset), buf, n, ctypes.byref(read))
        if ok and read.value > 0:
            stats["read"] += read.value
            data = buf.raw[:read.value]
            stats["compact"] += _count_compact(data)
            stats["utf16"] += _count_utf16(data)
        if read.value <= SCAN_OVERLAP:
            break
        offset += read.value - SCAN_OVERLAP


def _count_compact(buf):
    count = 0
    n = len(buf)
    i = 0
    while i + 44 <= n:
        if buf[i + 43] != 0x3D or not _is_base64_char(buf[i]):
            i += 1
            continue
        if i > 0 and _is_base64_char(buf[i - 1]):
            i += 1
            continue
        if i + 44 < n and (_is_base64_char(buf[i + 44]) or buf[i + 44] == 0x3D):
            i += 1
            continue
        ok = True
        for j in range(i, i + 43):
            if not _is_base64_char(buf[j]):
                ok = False
                break
        if ok:
            count += 1
        i += 44
    return count


def _count_utf16(buf):
    count = 0
    n = len(buf)
    i = 0
    while i + 88 <= n:
        if buf[i + 1] == 0 and buf[i + 87] == 0 and buf[i + 86] == 0x3D and _is_base64_char(buf[i]):
            ok = True
            for j in range(43):
                if buf[i + 2 * j + 1] != 0 or not _is_base64_char(buf[i + 2 * j]):
                    ok = False
                    break
            if ok:
                count += 1
        i += 2
    return count
