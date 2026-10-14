//go:build windows

package plugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procGetConsoleWindow         = kernel32.NewProc("GetConsoleWindow")
)

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitKillOnJobClose           = 0x2000
)

type jobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	AffectedProcessID       uint32
	AffectedJobID           uint32
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobIOCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobExtendedLimitInformation struct {
	BasicLimitInformation jobBasicLimitInformation
	IOInfo                jobIOCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// procHandle 用 Windows 作业对象托管插件进程：
// 句柄关闭时整棵进程树会被一并结束，避免脚本类插件留下孤儿进程。
// 作业对象创建失败时退化为空句柄，此时仅能保证直接子进程被回收。
type procHandle struct {
	job      windows.Handle
	released bool
}

func newProcHandle() *procHandle {
	handle, _, _ := procCreateJobObjectW.Call(0, 0)
	if handle == 0 {
		return &procHandle{}
	}

	info := jobExtendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	procSetInformationJobObject.Call(
		handle,
		uintptr(jobObjectExtendedLimitInformationClass),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)

	return &procHandle{job: windows.Handle(handle)}
}

func (p *procHandle) attach(cmd *exec.Cmd) error {
	if p == nil || p.job == 0 || cmd == nil || cmd.Process == nil {
		return nil
	}

	h, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()

	ret, _, callErr := procAssignProcessToJobObject.Call(uintptr(p.job), uintptr(h))
	if ret == 0 {
		return callErr
	}
	return nil
}

// release 关闭作业句柄；插件仍在运行时，其整棵进程树会被一并结束。
func (p *procHandle) release() {
	if p == nil || p.job == 0 || p.released {
		return
	}
	p.released = true
	_ = windows.CloseHandle(p.job)
}

// applyProcAttr 让子进程成为独立进程组的组长，
// 从而可以向该组单独发送 Ctrl-Break，而不影响网关自身。
// 若网关自身没有控制台（例如桌面 GUI），控制台类插件默认会弹出一个黑色控制台窗口，
// 这里显式加上 CREATE_NO_WINDOW 隐藏它；网关有控制台时保持共享控制台，优雅停止仍可用。
func applyProcAttr(cmd *exec.Cmd) {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP)
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd == 0 {
		flags |= windows.CREATE_NO_WINDOW
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: flags,
	}
}

// gracefulStop 尝试向插件进程组发送 Ctrl-Break（Windows 无 SIGTERM）。
// 若网关没有控制台（例如以服务方式运行），会返回错误，交由调用方强杀。
func gracefulStop(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
}

// wrapCommand Windows 无法直接用 CreateProcess 执行 .cmd/.bat，
// 需要经命令解释器转发（例如 Node/Python 脚本插件常见的启动壳）。
func wrapCommand(entry string, args []string) (string, []string) {
	switch strings.ToLower(filepath.Ext(entry)) {
	case ".cmd", ".bat":
		interpreter := os.Getenv("ComSpec")
		if interpreter == "" {
			interpreter = "cmd.exe"
		}
		return interpreter, append([]string{"/c", entry}, args...)
	}
	return entry, args
}