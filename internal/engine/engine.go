// Package engine runs one sync pass for a job and records the outcome.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/classify"
	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/rclone"
	"github.com/WentTheFox/FoxCloud/internal/secret"
	"github.com/WentTheFox/FoxCloud/internal/state"
)

// ErrPaused means the job is waiting for a human to resume it.
var ErrPaused = errors.New("job is paused")

// SyncError is a classified failure of a sync pass.
type SyncError struct{ classify.Failure }

func (e *SyncError) Error() string { return e.Message }

type Engine struct {
	Cfg    *config.Config
	Store  state.Store
	Rclone string
	Out    io.Writer
	Now    func() time.Time
}

type RunOptions struct {
	DryRun bool
	Force  bool
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Run performs one sync pass for the named job.
func (e *Engine) Run(ctx context.Context, name string, opts RunOptions) error {
	job, ok := e.Cfg.Job(name)
	if !ok {
		return fmt.Errorf("no job named %q", name)
	}
	if job.Role == config.RoleMount {
		return fmt.Errorf("job %q is a mount job; mounting is not implemented yet", name)
	}
	st, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	if st.Paused != nil {
		return fmt.Errorf("%w: %s (run `foxcloud resume %s` once fixed)", ErrPaused, st.Paused.Message, name)
	}
	if job.RequireMount != "" {
		if err := checkMounted(job.RequireMount); err != nil {
			return fmt.Errorf("required mount unavailable: %w", err)
		}
	}
	if err := os.MkdirAll(job.LocalPath, 0o755); err != nil {
		return err
	}

	env, err := e.remoteEnv(ctx)
	if err != nil {
		return err
	}
	resync := job.Role == config.RoleBisync && !st.Resynced
	if resync {
		fmt.Fprintln(e.Out, "first bisync run: establishing baseline with --resync (newer wins)")
	}
	args, err := rclone.Args(job, rclone.Options{
		DryRun: opts.DryRun, Force: opts.Force, Resync: resync, Now: e.now(),
	})
	if err != nil {
		return err
	}

	e.warnLongNames(ctx, job)

	st.LastAttempt = e.now()
	output, runErr := rclone.Run(ctx, e.Rclone, args, env, e.Out)
	if opts.DryRun {
		return runErr // a dry run must not change the recorded state
	}
	if runErr == nil {
		st.LastSuccess, st.LastFailure = e.now(), nil
		st.Resynced = st.Resynced || job.Role == config.RoleBisync
		return e.Store.Save(name, st)
	}
	if ctx.Err() != nil {
		return ctx.Err()
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

// Usage reports how full the remote storage is.
func (e *Engine) Usage(ctx context.Context) (rclone.Usage, error) {
	env, err := e.remoteEnv(ctx)
	if err != nil {
		return rclone.Usage{}, err
	}
	return rclone.About(ctx, e.Rclone, env)
}

// Resume clears a job's pause so syncing can continue.
func (e *Engine) Resume(name string) error {
	st, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	st.Paused = nil
	return e.Store.Save(name, st)
}

func (e *Engine) remoteEnv(ctx context.Context) ([]string, error) {
	key, err := secret.Resolve(ctx, e.Cfg.Crypt.KeyCommand)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, errors.New("crypt.key_command is not set; refusing to sync unencrypted")
	}
	pw, salt, err := secret.Derive(key)
	if err != nil {
		return nil, err
	}
	obsPw, err := rclone.Obscure(ctx, e.Rclone, pw)
	if err != nil {
		return nil, err
	}
	obsSalt, err := rclone.Obscure(ctx, e.Rclone, salt)
	if err != nil {
		return nil, err
	}
	remote, err := e.resolveRemote(ctx)
	if err != nil {
		return nil, err
	}
	cfg := *e.Cfg
	cfg.Remote = remote
	return rclone.Env(&cfg, obsPw, obsSalt), nil
}
