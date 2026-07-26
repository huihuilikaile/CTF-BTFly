//go:build !windows

package main

import "os/exec"

// prepareProcess 在非 Windows 平台无需附加进程创建标志。
// 空实现让 DesktopService 保持一套跨平台启动流程。
func prepareProcess(command *exec.Cmd) {}
