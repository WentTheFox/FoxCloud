package engine

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/delta"
	"github.com/WentTheFox/FoxCloud/internal/journal"
	"github.com/WentTheFox/FoxCloud/internal/rclone"
	"github.com/WentTheFox/FoxCloud/internal/secret"
)

// JournalClient builds the client for the shared change log: the log is
// reached over the same SSH account as the data, and its lines are encrypted
// with a key derived from the crypt master key.
func (e *Engine) JournalClient(ctx context.Context) (*journal.Client, error) {
	if !e.Cfg.Journal.Enabled {
		return nil, journal.ErrNotConfigured
	}
	jpath := e.Cfg.Journal.LogPath()
	if !journal.ValidPath(jpath) {
		return nil, fmt.Errorf("[journal] path %q may only use letters, digits and . _ / -", jpath)
	}
	key, err := secret.Resolve(ctx, e.Cfg.Crypt.KeyCommand)
	if err != nil {
		return nil, err
	}
	codec, err := journal.NewCodec(key)
	if err != nil {
		return nil, err
	}
	remote, err := e.resolveRemote(ctx)
	if err != nil {
		return nil, err
	}
	return &journal.Client{
		Codec:     codec,
		Transport: &journal.SSH{Args: sshArgs(remote), Path: jpath},
		Machine:   e.Cfg.Journal.MachineName(),
		StateFile: filepath.Join(e.Store.Dir, "journal.json"),
	}, nil
}

// sshArgs is the ssh command line (up to the host) for the configured remote.
// The host key is checked against the same known_hosts file rclone uses.
func sshArgs(r config.Remote) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes"}
	if r.KnownHostsFile != "" {
		args = append(args, "-o", "UserKnownHostsFile="+r.KnownHostsFile)
	}
	if r.Port != 0 {
		args = append(args, "-p", strconv.Itoa(r.Port))
	}
	if r.KeyFile != "" {
		args = append(args, "-i", r.KeyFile, "-o", "IdentitiesOnly=yes")
	}
	return append(args, "-l", r.User, r.Host)
}

// JournalRecords describes the operations just applied to the remote, for the
// shared log. Paths are given as they appear in the remote's crypt root, so
// machines with different local folders agree. For a bisync job only uploads
// and folders were applied (deletions and moves wait for a full pass), so only
// those are recorded: the log must never promise something that did not happen.
func JournalRecords(job config.Job, ops delta.Ops) []journal.Record {
	remote := func(rel string) string { return path.Join(strings.Trim(job.RemotePath, "/"), rel) }
	push := job.Role == config.RolePush
	var recs []journal.Record
	for _, m := range ops.Moves {
		if push {
			recs = append(recs, journal.Record{Op: journal.OpMove, Path: remote(m.From), To: remote(m.To), Dir: m.Dir})
		}
	}
	if push {
		for _, p := range ops.DeleteFiles {
			recs = append(recs, journal.Record{Op: journal.OpDelete, Path: remote(p)})
		}
		for _, p := range ops.DeleteDirs {
			recs = append(recs, journal.Record{Op: journal.OpRmdir, Path: remote(p), Dir: true})
		}
	}
	for _, p := range ops.Mkdirs {
		recs = append(recs, journal.Record{Op: journal.OpMkdir, Path: remote(p), Dir: true})
	}
	uploads := append([]string(nil), ops.Uploads...)
	if !push {
		// A moved file was uploaded under its new name.
		for _, m := range ops.Moves {
			if !m.Dir {
				uploads = append(uploads, m.To)
			}
		}
	}
	sort.Strings(uploads)
	for _, p := range uploads {
		r := journal.Record{Op: journal.OpUpload, Path: remote(p)}
		if fi, err := os.Lstat(filepath.Join(job.LocalPath, filepath.FromSlash(p))); err == nil {
			r.Size, r.Mod = fi.Size(), fi.ModTime().UnixNano()
		}
		recs = append(recs, r)
	}
	return recs
}

// Fetch downloads named files from the remote into a bisync or pull job's
// local folder, without listing either side: rclone is given the names and
// looks only at those, so a file the remote does not have is skipped. With
// overwrite false an existing local file is never replaced (a bisync job leaves
// those to bisync, which can tell a conflict from an update); with it true an
// older local file is replaced after being moved to the job's backup folder,
// as a pull job does.
func (e *Engine) Fetch(ctx context.Context, name string, paths []string, overwrite bool) error {
	job, ok := e.Cfg.Job(name)
	if !ok {
		return fmt.Errorf("no job named %q", name)
	}
	if job.Role != config.RoleBisync && job.Role != config.RolePull {
		return fmt.Errorf("job %q (%s) does not download", name, job.Role)
	}
	if len(paths) == 0 {
		return nil
	}
	st, err := e.Store.Load(name)
	if err != nil {
		return err
	}
	if st.Paused != nil {
		return fmt.Errorf("%w: %s", ErrPaused, st.Paused.Message)
	}
	env, err := e.remoteEnv(ctx)
	if err != nil {
		return err
	}
	args := []string{"copy", rclone.CryptRemote(job.RemotePath), job.LocalPath, "--no-traverse", "-v"}
	if overwrite {
		args = append(args, "--update", "--backup-dir", job.BackupDir, "--suffix", "."+e.now().Format("20060102-150405"))
	} else {
		args = append(args, "--ignore-existing")
	}
	return e.withList(paths, func(list string) error {
		_, err := rclone.Run(ctx, e.Rclone, append(args, "--files-from-raw", list), env, e.Out, nil)
		return err
	})
}
