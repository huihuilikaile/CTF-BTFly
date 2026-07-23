//go:build !windows

package main

import "os/exec"

func prepareProcess(command *exec.Cmd) {}
