package rclone

import (
	"fmt"
	"strconv"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/config"
)

// Options tweak a single run.
type Options struct {
	DryRun bool
	// Force bypasses rclone's max-delete safety check.
	Force bool
	// Resync makes a bisync run rebuild its baseline (newer file wins).
	Resync bool
	Now    time.Time
}

// Args builds the rclone command line (without the binary name) for a job.
func Args(j config.Job, o Options) ([]string, error) {
	remote := CryptRemote(j.RemotePath)

	common := []string{"--create-empty-src-dirs", "--stats", "1m", "-v"}
	if j.FilterFile != "" {
		common = append(common, "--filter-from", j.FilterFile)
	}
	if o.DryRun {
		common = append(common, "--dry-run")
	}
	if o.Force {
		common = append(common, "--force")
	}
	suffix := "." + o.Now.Format("20060102-150405")
	maxDelete := strconv.Itoa(j.MaxDelete)

	var args []string
	switch j.Role {
	case config.RolePush:
		// The remote keeps its own snapshots, so no backup dir is needed here.
		args = []string{"sync", j.LocalPath, remote, "--max-delete", maxDelete}
	case config.RolePull:
		// copy never deletes locally; anything it would overwrite is moved aside first.
		args = []string{"copy", remote, j.LocalPath, "--update",
			"--backup-dir", j.BackupDir, "--suffix", suffix}
	case config.RoleBisync:
		args = []string{"bisync", remote, j.LocalPath,
			"--resilient", "--recover", "--max-lock", "2m",
			"--conflict-resolve", "newer", "--max-delete", maxDelete,
			"--backup-dir2", j.BackupDir}
		if o.Resync {
			args = append(args, "--resync", "--resync-mode", "newer")
		}
	default:
		return nil, fmt.Errorf("role %q has no rclone sync command", j.Role)
	}
	return append(args, common...), nil
}
