package rclone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// stopGrace is how long rclone gets to wind down after being asked to stop
// (a mount unmounts, a transfer finishes cleanly) before it is killed.
const stopGrace = 15 * time.Second

// maxCaptured bounds how much output is kept for failure classification;
// a verbose run over many files can be large and the cause is near the end.
const maxCaptured = 256 << 10

type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > maxCaptured {
		t.b = t.b[len(t.b)-maxCaptured:]
	}
	return len(p), nil
}

// Output runs rclone and returns its stdout alone, for commands whose output
// is parsed. Stderr is returned in the error on failure.
func Output(ctx context.Context, bin string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, fmt.Errorf("rclone %s: %w: %s", args[0], err, strings.TrimSpace(string(ee.Stderr)))
	}
	return out, err
}

// Run executes rclone, streaming its output to out while keeping the tail of
// it for classification. env is appended to the process environment. If onMsg
// is set, rclone must have been given --use-json-log: its lines are rendered
// as plain text and each message is passed to onMsg.
func Run(ctx context.Context, bin string, args, env []string, out io.Writer, onMsg func(string)) (output string, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	// A plain kill leaves a FUSE mount behind, stale and unreadable, so ask
	// rclone to stop first. Windows cannot signal a process this way.
	if runtime.GOOS != "windows" {
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		cmd.WaitDelay = stopGrace
	}
	var tail tailBuffer
	w := io.Writer(io.MultiWriter(out, &tail))
	if onMsg != nil {
		w = &jsonLog{out: w, onMsg: onMsg}
	}
	cmd.Stdout, cmd.Stderr = w, w
	err = cmd.Run()
	return string(tail.b), err
}
