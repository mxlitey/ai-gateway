//go:build windows

package mcp

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideLocalWindow 以 GUI 方式运行时，python 会弹出黑色控制台窗口，这里统一隐藏；
// 同时让子进程自成一个进程组，便于单独结束。
func hideLocalWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP
}
