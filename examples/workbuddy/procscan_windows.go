//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 从正在运行的桌面端进程内存里找回 atRestSecretKey 派生出的密钥。
//
// 背景：该密钥编译在客户端原生模块里，磁盘上没有明文，只存在于运行中的进程内存。
// 判据：sha256(候选字符串)[:16] 等于 keyblob 里 static-v1 槽的 protectorKeyId，
// 因此候选不需要理解上下文，命中即可信。
const (
	memCommit  = 0x1000
	memPrivate = 0x20000

	pageNoAccess        = 0x01
	pageReadOnly        = 0x02
	pageReadWrite       = 0x04
	pageWriteCopy       = 0x08
	pageExecuteRead     = 0x20
	pageExecuteReadWrite = 0x40
	pageExecuteWriteCopy = 0x80
	pageGuard           = 0x100

	scanChunk   = 4 << 20
	scanOverlap = 128
)

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procVirtualQueryEx    = kernel32.NewProc("VirtualQueryEx")
	procReadProcessMemory = kernel32.NewProc("ReadProcessMemory")
)

// MEMORY_BASIC_INFORMATION（64 位布局，48 字节）
type memoryBasicInformation struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	PartitionID       uint16
	Alignment1        uint16
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
	Alignment2        uint32
}

// findProtectorKey 返回 protector key（32 字节）。
func findProtectorKey(protectorKeyID string) ([]byte, error) {
	want := strings.ToLower(strings.TrimSpace(protectorKeyID))
	if want == "" {
		return nil, fmt.Errorf("keyblob 里没有 protectorKeyId")
	}

	pids, err := desktopProcessIDs()
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("没找到正在运行的桌面端进程：密钥只存在于进程内存，请先打开 WorkBuddy 并保持登录")
	}

	denied := 0
	for _, pid := range pids {
		h, err := windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION, false, pid)
		if err != nil {
			denied++
			continue
		}
		log.Printf("[import] 扫描进程 pid=%d 的内存", pid)
		key := scanProcessForKey(h, want)
		_ = windows.CloseHandle(h)
		if key != nil {
			return key, nil
		}
	}
	if denied == len(pids) {
		return nil, fmt.Errorf("无法读取桌面端进程内存（权限不足）：请以管理员身份运行网关，或关闭桌面端的额外保护后重试")
	}
	return nil, fmt.Errorf("已扫描 %d 个桌面端进程但没有找回密钥：请先打开 WorkBuddy 桌面端界面（让它解密一次登录态），"+
		"稍等几秒再试；也可以改用 OAuth 授权登录", len(pids))
}

func desktopProcessIDs() ([]uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("枚举进程失败: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	var out []uint32
	self := uint32(os.Getpid())
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil, nil
	}
	for {
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		// 插件自己的可执行文件也叫 workbuddy.exe，别把自己算进去
		if entry.ProcessID != self {
			switch name {
			case "workbuddy.exe", "workbuddyai.exe", "codebuddy.exe":
				out = append(out, entry.ProcessID)
			}
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return out, nil
}

// scanProcessForKey 两遍扫描：先只扫可写私有内存（JS 堆在这里，最快），全都没中再放宽。
func scanProcessForKey(h windows.Handle, want string) []byte {
	for _, onlyPrivate := range []bool{true, false} {
		var addr uintptr
		for {
			var mbi memoryBasicInformation
			ret, _, _ := procVirtualQueryEx.Call(
				uintptr(h), addr, uintptr(unsafe.Pointer(&mbi)), unsafe.Sizeof(mbi))
			if ret == 0 {
				break
			}
			next := mbi.BaseAddress + mbi.RegionSize
			if mbi.State == memCommit && mbi.RegionSize > 0 && readableProtect(mbi.Protect) &&
				(!onlyPrivate || mbi.Type == memPrivate) {
				if key := scanRegion(h, mbi.BaseAddress, mbi.RegionSize, want); key != nil {
					return key
				}
			}
			if next <= addr {
				break
			}
			addr = next
		}
	}
	return nil
}

func readableProtect(p uint32) bool {
	if p&pageGuard != 0 || p&pageNoAccess != 0 {
		return false
	}
	switch p & 0xff {
	case pageReadOnly, pageReadWrite, pageWriteCopy, pageExecuteRead, pageExecuteReadWrite, pageExecuteWriteCopy:
		return true
	}
	return false
}

func scanRegion(h windows.Handle, base, size uintptr, want string) []byte {
	buf := make([]byte, scanChunk)
	offset := uintptr(0)
	for offset < size {
		n := uintptr(len(buf))
		if size-offset < n {
			n = size - offset
		}

		var read uintptr
		ok, _, _ := procReadProcessMemory.Call(
			uintptr(h), base+offset, uintptr(unsafe.Pointer(&buf[0])), n,
			uintptr(unsafe.Pointer(&read)))
		if ok != 0 && read > 0 {
			if key := scanBuffer(buf[:read], want); key != nil {
				return key
			}
		}
		if read <= scanOverlap {
			break
		}
		offset += read - scanOverlap
	}
	return nil
}

// scanBuffer 在一个内存块里找候选密钥：
// 先找 44 字符 base64（紧凑与 UTF-16 两种排布），再按 8 字节栅格找已派生的 key。
func scanBuffer(buf []byte, want string) []byte {
	if key := scanCompact(buf, want); key != nil {
		return key
	}
	if key := scanUTF16(buf, want); key != nil {
		return key
	}
	return scanWindows(buf, want, 8)
}

// scanCompact 找 43 个 base64 字符 + '='（共 44 字符）的候选。
func scanCompact(buf []byte, want string) []byte {
	n := len(buf)
	for i := 0; i+44 <= n; i++ {
		if buf[i+43] != '=' || !isBase64Char(buf[i]) {
			continue
		}
		if i > 0 && isBase64Char(buf[i-1]) {
			continue
		}
		if i+44 < n && (isBase64Char(buf[i+44]) || buf[i+44] == '=') {
			continue
		}
		ok := true
		for j := i; j < i+43; j++ {
			if !isBase64Char(buf[j]) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if key := checkCandidate(buf[i:i+44], want); key != nil {
			return key
		}
		i += 43
	}
	return nil
}

// scanUTF16 找 UTF-16LE 排布的同一个候选（JS 字符串是 UTF-16）。
func scanUTF16(buf []byte, want string) []byte {
	n := len(buf)
	for i := 0; i+88 <= n; i += 2 {
		if buf[i+1] != 0 || buf[i+87] != 0 || buf[i+86] != '=' || !isBase64Char(buf[i]) {
			continue
		}
		if i >= 2 && buf[i-1] == 0 && isBase64Char(buf[i-2]) {
			continue
		}
		if i+89 < n && buf[i+88] != 0 && isBase64Char(buf[i+88]) {
			continue
		}
		ok := true
		for j := 0; j < 43; j++ {
			if buf[i+2*j+1] != 0 || !isBase64Char(buf[i+2*j]) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		cand := make([]byte, 44)
		for j := 0; j < 43; j++ {
			cand[j] = buf[i+2*j]
		}
		cand[43] = '='
		if key := checkCandidate(cand, want); key != nil {
			return key
		}
	}
	return nil
}

// checkCandidate 校验候选。
//
// 客户端是两层派生：key = sha256(候选)，keyId = sha256(key).hex[:16]，
// 所以判断要连着做两次 sha256；命中的候选本身作为解密用的 key。
func checkCandidate(cand []byte, want string) []byte {
	key := sha256.Sum256(cand)
	fingerprint := sha256.Sum256(key[:])
	if hex.EncodeToString(fingerprint[:])[:16] != want {
		return nil
	}
	// 附加约束：候选必须是 32 字节的规范 base64（与客户端 isCanonical32ByteBase64 一致）
	raw, err := base64.StdEncoding.DecodeString(string(cand))
	if err != nil || len(raw) != 32 {
		return nil
	}
	out := make([]byte, 32)
	copy(out, key[:])
	return out
}

// scanWindows 按栅格找「已经在内存里的派生 key 本身」（原生 Buffer 至少 16 字节对齐，
// 先按 8 字节栅格扫；命中率很高而且比逐字节快 8 倍）。
func scanWindows(buf []byte, want string, stride int) []byte {
	for i := 0; i+32 <= len(buf); i += stride {
		window := buf[i : i+32]
		fingerprint := sha256.Sum256(window)
		if hex.EncodeToString(fingerprint[:])[:16] == want {
			out := make([]byte, 32)
			copy(out, window)
			return out
		}
	}
	return nil
}

func isBase64Char(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	case b == '+', b == '/':
		return true
	}
	return false
}

// ---------- 诊断：统计扫描过程中看到了什么 ----------

type scanStats struct {
	Regions   int
	ReadBytes uint64
	Compact   int
	UTF16     int
	Samples   []string
}

// probeProcesses 统计各进程的可读区域与候选分布，用于排查「扫不到密钥」。
func probeProcesses(want string) ([]string, error) {
	pids, err := desktopProcessIDs()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, pid := range pids {
		h, err := windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION, false, pid)
		if err != nil {
			out = append(out, fmt.Sprintf("pid=%d 打不开进程: %v", pid, err))
			continue
		}
		stats := &scanStats{}
		probeProcess(h, want, stats)
		_ = windows.CloseHandle(h)

		line := fmt.Sprintf("pid=%d 区域=%d 读取=%.1fMB 紧凑候选=%d UTF16候选=%d",
			pid, stats.Regions, float64(stats.ReadBytes)/(1<<20), stats.Compact, stats.UTF16)
		for _, s := range stats.Samples {
			line += "\n      " + s
		}
		out = append(out, line)
	}
	return out, nil
}

func probeProcess(h windows.Handle, want string, stats *scanStats) {
	var addr uintptr
	for {
		var mbi memoryBasicInformation
		ret, _, _ := procVirtualQueryEx.Call(
			uintptr(h), addr, uintptr(unsafe.Pointer(&mbi)), unsafe.Sizeof(mbi))
		if ret == 0 {
			break
		}
		next := mbi.BaseAddress + mbi.RegionSize
		if mbi.State == memCommit && mbi.RegionSize > 0 && readableProtect(mbi.Protect) {
			stats.Regions++
			probeRegion(h, mbi.BaseAddress, mbi.RegionSize, want, stats)
		}
		if next <= addr {
			break
		}
		addr = next
	}
}

func probeRegion(h windows.Handle, base, size uintptr, want string, stats *scanStats) {
	buf := make([]byte, scanChunk)
	offset := uintptr(0)
	for offset < size {
		n := uintptr(len(buf))
		if size-offset < n {
			n = size - offset
		}
		var read uintptr
		ok, _, _ := procReadProcessMemory.Call(
			uintptr(h), base+offset, uintptr(unsafe.Pointer(&buf[0])), n,
			uintptr(unsafe.Pointer(&read)))
		if ok != 0 && read > 0 {
			stats.ReadBytes += uint64(read)
			countCandidates(buf[:read], want, stats)
		}
		if read <= scanOverlap {
			break
		}
		offset += read - scanOverlap
	}
}

func countCandidates(buf []byte, want string, stats *scanStats) {
	n := len(buf)
	for i := 0; i+44 <= n; i++ {
		if buf[i+43] != '=' || !isBase64Char(buf[i]) {
			continue
		}
		if i > 0 && isBase64Char(buf[i-1]) {
			continue
		}
		if i+44 < n && (isBase64Char(buf[i+44]) || buf[i+44] == '=') {
			continue
		}
		ok := true
		for j := i; j < i+43; j++ {
			if !isBase64Char(buf[j]) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		stats.Compact++
		cand := buf[i : i+44]
		if len(stats.Samples) < 6 {
			inner := sha256.Sum256(cand)
			outer := sha256.Sum256(inner[:])
			got := hex.EncodeToString(outer[:])[:16]
			stats.Samples = append(stats.Samples,
				fmt.Sprintf("紧凑 %s -> %s %s", string(cand), got, matchMark(got, want)))
		}
		i += 43
	}
}

func matchMark(got, want string) string {
	if got == want {
		return "== 命中!"
	}
	return ""
}