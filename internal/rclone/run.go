package rclone

import (
	"context"
	"io"
	"os"
	"os/exec"
)

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

// Run executes rclone, streaming its output to out while keeping the tail of
// it for classification. env is appended to the process environment.
func Run(ctx context.Context, bin string, args, env []string, out io.Writer) (output string, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var tail tailBuffer
	w := io.MultiWriter(out, &tail)
	cmd.Stdout, cmd.Stderr = w, w
	err = cmd.Run()
	return string(tail.b), err
}
