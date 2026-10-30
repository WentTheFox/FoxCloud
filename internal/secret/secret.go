// Package secret fetches secrets by running a user-supplied command, so they
// can live in the OS keychain rather than in a config file.
package secret

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// FromCommand runs command through the platform shell and returns its trimmed
// stdout. An empty command yields an empty secret.
func FromCommand(ctx context.Context, command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", nil
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// stderr is deliberately left out of the message in case it echoes a secret.
		return "", fmt.Errorf("secret command failed: %w", err)
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}
