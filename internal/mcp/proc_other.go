//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// hideLocalWindow 非 Windows 平台没有控制台窗口概念，只把子进程放进独立进程组。
func hideLocalWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
