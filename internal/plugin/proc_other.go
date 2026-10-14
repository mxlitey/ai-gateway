//go:build !windows

package plugin

import (
	"os/exec"
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