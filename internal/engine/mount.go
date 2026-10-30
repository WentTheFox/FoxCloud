package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/classify"
	"github.com/WentTheFox/FoxCloud/internal/rclone"
	"github.com/WentTheFox/FoxCloud/internal/state"
)

// Readiness is polled, which is the one place FoxCloud polls: rclone gives a
// foreground mount no signal when it is up, and the OS has no event for a
// drive or mount point appearing that works the same everywhere. The poll is
// bounded (readyTimeout) and stops as soon as the mount is up or rclone exits.
const (
	readyEvery   = 250 * time.Millisecond
	readyTimeout = 30 * time.Second
)

// Mount runs a mount job's rclone in the foreground until ctx is cancelled or
// rclone exits. onReady is called once the mount point is usable. It returns
// nil only when ctx was cancelled; rclone stopping by itself is an error,
// classified where the output allows.
func (e *Engine) Mount(ctx context.Context, name string, onReady func()) error {
	job, ok := e.Cfg.Job(name)
	if !ok {
		return fmt.Errorf("no job named %q", name)
	}
	st, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	if st.Paused != nil {
		return fmt.Errorf("%w: %s (run `foxcloud resume %s` once fixed)", ErrPaused, st.Paused.Message, name)
	}
	args, err := rclone.MountArgs(job)
	if err != nil {
		return err
	}
	// rclone wants an existing empty folder on Unix; on Windows the mount point
	// is a drive letter (or a folder it must create itself).
	if runtime.GOOS != "windows" {
		if err := os.MkdirAll(job.MountPoint, 0o755); err != nil {
			return err
		}
	}
	env, err := e.remoteEnv(ctx)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	exited := make(chan struct{})
	go func() {
		t := time.NewTicker(readyEvery)
		defer t.Stop()
		deadline := time.After(readyTimeout)
		for {
			select {
			case <-exited:
				return
			case <-deadline:
				return
			case <-t.C:
				if checkMounted(job.MountPoint) == nil {
					if onReady != nil {
						onReady()
					}
					return
				}
			}
		}
	}()

	st.LastAttempt = e.now()
	output, runErr := rclone.Run(runCtx, e.Rclone, args, env, e.Out, nil)
	close(exited)
	if ctx.Err() != nil {
		return nil
	}
	if runErr == nil {
		runErr = errors.New("rclone mount exited")
	}

	f := classify.Classify(output)
	rec := &state.Failure{Kind: string(f.Kind), Message: f.Message, At: e.now()}
	st.LastFailure = rec
	if f.Pause {
		st.Paused = rec
	}
	if err := e.Store.Save(name, st); err != nil {
		return errors.Join(&SyncError{f}, err)
	}
	return &SyncError{f}
}
