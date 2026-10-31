//go:build !windows

// Package proc holds small helpers for starting child processes.
package proc

import "os/exec"

// Hide is a no-op outside Windows.
func Hide(cmd *exec.Cmd) *exec.Cmd { return cmd }
