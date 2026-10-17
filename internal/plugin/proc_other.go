//go:build !windows

package plugin

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// procHandle 在非 Windows 平台无作业对象，进程组已足够覆盖回收需求。
type procHandle struct{}

func newProcHandle() *procHandle { return &procHandle{} }

func (p *procHandle) attach(cmd *exec.Cmd) error { return nil }

func (p *procHandle) release() {}

// applyProcAttr 让子进程进入独立进程组，便于整组回收。
func applyProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// gracefulStop 向插件进程组发送 SIGTERM。
func gracefulStop(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// wrapCommand 非 Windows 平台直接执行 entry。
func wrapCommand(entry string, args []string) (string, []string) {
	return entry, args
}

// ensureExecutable 补上可执行位：从 zip 解压或经卷复制过来的插件常丢失该权限，
// 会导致 exec 报 permission denied。仅对实际存在的普通文件操作，失败时忽略（交由 exec 报错）。
func ensureExecutable(path string) {
	if !strings.ContainsAny(path, `/\`) {
		return // 裸命令名交给 PATH 查找，不处理
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	if info.Mode().Perm()&0o111 != 0 {
		return
	}
	_ = os.Chmod(path, info.Mode().Perm()|0o755)
}