//go:build windows

// Package proc holds small helpers for starting child processes.
package proc

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// Hide stops a child console program from opening a window. foxcloudd and the
// tray are GUI-subsystem programs with no console, so without this Windows
// gives every console child (rclone, ssh, powershell) a new visible one.
func Hide(cmd *exec.Cmd) *exec.Cmd {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	return cmd
}
