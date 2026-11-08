package rclone

import "strings"

// MountChange is a change a mount made on the remote, read from rclone's log.
// Paths are as rclone prints them: decrypted, relative to the mount's root.
type MountChange struct {
	// Upload is a file that now exists on the remote (new or replaced).
	// Move is a file that was renamed there (Path is the old name, To the new).
	Kind     MountChangeKind
	Path, To string
}

// MountChangeKind says what a MountChange is.
type MountChangeKind int

const (
	MountUpload MountChangeKind = iota + 1
	MountMove
)

// ParseMountRecord reads one log record of `rclone mount -v`.
//
// Only lines that say the remote itself changed are used. At -v rclone logs
// finished uploads and server-side renames of files, but nothing for mkdir,
// rmdir, deleting files or renaming folders (their only lines describe the
// local cache), so those cannot be told apart reliably and are left out. The
// wording is rclone's and not a stable interface; what is not recognised is
// simply not reported.
func ParseMountRecord(r LogRecord) (MountChange, bool) {
	if r.Level != "info" && r.Level != "notice" || r.Object == "" {
		return MountChange{}, false
	}
	msg := ansi.ReplaceAllString(r.Msg, "")
	switch {
	case msg == "Copied (new)", msg == "Copied (replaced existing)":
		return MountChange{Kind: MountUpload, Path: r.Object}, true
	case strings.HasPrefix(msg, "Moved (server-side) to: "):
		to := strings.TrimPrefix(msg, "Moved (server-side) to: ")
		if to == "" {
			return MountChange{}, false
		}
		return MountChange{Kind: MountMove, Path: r.Object, To: to}, true
	}
	return MountChange{}, false
}
