package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/WentTheFox/FoxCloud/internal/classify"
	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/delta"
	"github.com/WentTheFox/FoxCloud/internal/rclone"
)

// ErrNeedsFullPass means the changes are not something to do file by file: the
// watcher lost events, or there are more deletions than max_delete allows. The
// caller runs a normal pass, which has the safety checks.
var ErrNeedsFullPass = errors.New("a full pass is needed")

var dirMissing = regexp.MustCompile(`(?i)directory not found|couldn't find|not found`)

// Apply carries a few file changes over to the remote without a full pass.
//
// For a push job (the local folder is the truth) it uploads new and changed
// files, creates folders, and applies moves and deletions. For a bisync job
// only uploads happen, and only over files that are not newer on the remote
// (--update): another machine may have changed or removed the same file, and
// deciding that is bisync's job, so deletions and moves wait for the next full
// pass. It does not touch the recorded sync state: only a full pass can say
// the two sides match.
func (e *Engine) Apply(ctx context.Context, name string, ops delta.Ops) error {
	job, ok := e.Cfg.Job(name)
	if !ok {
		return fmt.Errorf("no job named %q", name)
	}
	if job.Role != config.RolePush && job.Role != config.RoleBisync {
		return fmt.Errorf("job %q (%s) is not synced file by file", name, job.Role)
	}
	if ops.Rescan {
		return ErrNeedsFullPass
	}
	st, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	if st.Paused != nil {
		return fmt.Errorf("%w: %s (run `foxcloud resume %s` once fixed)", ErrPaused, st.Paused.Message, name)
	}
	push := job.Role == config.RolePush
	if push && job.MaxDelete > 0 && ops.Deletes > job.MaxDelete {
		return ErrNeedsFullPass // rclone's own max-delete check decides
	}
	if job.RequireMount != "" {
		if err := checkMounted(job.RequireMount); err != nil {
			return fmt.Errorf("required mount unavailable: %w", err)
		}
	}
	env, err := e.remoteEnv(ctx)
	if err != nil {
		return err
	}
	remote := func(rel string) string {
		return rclone.CryptRemote(path.Join(job.RemotePath, rel))
	}
	run := func(args ...string) (string, error) {
		out, err := rclone.Run(ctx, e.Rclone, append(args, "-v"), env, e.Out, nil)
		if err != nil && ctx.Err() == nil {
			f := classify.Classify(out)
			if f.Kind != classify.KindUnknown {
				return out, &SyncError{f}
			}
		}
		return out, err
	}

	uploads := append([]string(nil), ops.Uploads...)
	if push {
		for _, m := range ops.Moves {
			_, err := run("moveto", remote(m.From), remote(m.To))
			if err == nil {
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Not on the remote under the old name (never uploaded, say): treat
			// it as new instead.
			if m.Dir {
				return ErrNeedsFullPass
			}
			uploads = append(uploads, m.To)
		}
		if len(ops.DeleteFiles) > 0 {
			if err := e.withList(ops.DeleteFiles, func(list string) error {
				_, err := run("delete", remote(""), "--files-from-raw", list)
				return err
			}); err != nil {
				return err
			}
		}
		for _, d := range ops.DeleteDirs {
			if out, err := run("purge", remote(d)); err != nil && !dirMissing.MatchString(out) {
				return err
			}
		}
	} else if len(ops.Moves) > 0 {
		// A moved file is a new path as far as an upload goes; the old path's
		// removal is left to bisync.
		for _, m := range ops.Moves {
			if !m.Dir {
				uploads = append(uploads, m.To)
			}
		}
		if len(ops.Moves) > 0 && hasDirMove(ops.Moves) {
			return ErrNeedsFullPass
		}
	}

	// Only files that are still there: one deleted in the meantime is not an error.
	var existing []string
	for _, rel := range uploads {
		if fi, err := os.Lstat(filepath.Join(job.LocalPath, filepath.FromSlash(rel))); err == nil && fi.Mode().IsRegular() {
			existing = append(existing, rel)
		}
	}
	sort.Strings(existing)
	if len(existing) > 0 {
		args := []string{"copy", job.LocalPath, remote(""), "--no-traverse"}
		if !push {
			args = append(args, "--update")
		}
		if err := e.withList(existing, func(list string) error {
			_, err := run(append(args, "--files-from-raw", list)...)
			return err
		}); err != nil {
			return err
		}
	}

	// Folders that hold no uploaded file: the copy above creates the rest.
	for _, d := range ops.Mkdirs {
		covered := false
		for _, u := range existing {
			if strings.HasPrefix(u, d+"/") {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		if _, err := run("mkdir", remote(d)); err != nil {
			return err
		}
	}
	return nil
}

func hasDirMove(ms []delta.Move) bool {
	for _, m := range ms {
		if m.Dir {
			return true
		}
	}
	return false
}

// withList writes the paths one per line to a temporary file for
// --files-from-raw (which takes names literally, with no pattern syntax).
func (e *Engine) withList(paths []string, fn func(list string) error) error {
	f, err := os.CreateTemp("", "foxcloud-files-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(strings.Join(paths, "\n") + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fn(f.Name())
}
