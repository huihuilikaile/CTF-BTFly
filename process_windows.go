//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// prepareProcess 在 Windows 上使用 CREATE_NO_WINDOW 启动后台 daemon，
// 避免桌面应用启动时额外弹出命令行窗口。
func prepareProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
}
