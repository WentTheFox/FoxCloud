package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/names"
	"github.com/WentTheFox/FoxCloud/internal/rclone"
)

// longNames lists the paths under the job's local folder (honouring its filter
// file) that the remote would reject once encrypted.
func (e *Engine) longNames(ctx context.Context, job config.Job) ([]string, error) {
	args := []string{"lsf", "-R"}
	if job.FilterFile != "" {
		args = append(args, "--filter-from", job.FilterFile)
	}
	args = append(args, job.LocalPath)
	out, err := rclone.Output(ctx, e.Rclone, args, nil)
	if err != nil {
		return nil, err
	}
	paths := strings.Split(string(out), "\n")
	return names.TooLong(paths, e.Cfg.Crypt.EncryptFilenames, e.Cfg.Crypt.EncryptDirectoryNames), nil
}

// CheckNames reports the paths of a job that would be rejected by the remote
// because a name is too long once encrypted.
func (e *Engine) CheckNames(ctx context.Context, name string) ([]string, error) {
	job, ok := e.Cfg.Job(name)
	if !ok {
		return nil, fmt.Errorf("no job named %q", name)
	}
	if job.Role == config.RoleMount {
		return nil, fmt.Errorf("job %q is a mount job and has no local folder to check", name)
	}
	return e.longNames(ctx, job)
}

// warnLongNames prints a heads-up before a sync. A failed check is not fatal:
// the sync is still worth running, rclone will report the real problem.
func (e *Engine) warnLongNames(ctx context.Context, job config.Job) {
	bad, err := e.longNames(ctx, job)
	if err != nil || len(bad) == 0 {
		return
	}
	fmt.Fprintf(e.Out, "warning: %d path(s) have a name longer than %d bytes and will not upload (run `foxcloud check-names %s` for the list):\n",
		len(bad), names.MaxPlainLen(), job.Name)
	for i, p := range bad {
		if i == 3 {
			fmt.Fprintf(e.Out, "  ... and %d more\n", len(bad)-3)
			break
		}
		fmt.Fprintf(e.Out, "  %s\n", p)
	}
}
