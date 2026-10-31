package rclone

import "strings"

// File operations a pass reports; they match the history package's names.
const (
	FileNew      = "new"
	FileModified = "modified"
	FileDeleted  = "deleted"
	FileFolder   = "folder"
)

// LogRecord is one of rclone's --use-json-log lines.
type LogRecord struct {
	Level, Object, Msg string
}

// FileEvent is one file (or folder) a pass changed, with the way it went.
type FileEvent struct {
	Op   string
	Path string
	Dir  bool
	// Way is Upload (changed on the remote) or Download (changed here).
	Way Direction
}

// FileTracker turns the per-file lines of one pass into events. It follows
// rclone's wording, which is not a stable interface, so anything it does not
// recognise is simply not reported.
//
// With a backup folder, rclone first moves the old file aside ("Moved
// (server-side)") and then copies the new one as "Copied (new)"; a file that
// was only deleted shows "Moved into backup dir". The tracker uses that order
// to tell a changed file from a new one and a deletion from a copy.
type FileTracker struct {
	way     Direction
	pending map[string]bool
}

// NewFileTracker starts a pass in the given direction: Upload for a push,
// Download for a pull. A bisync pass starts with either and is corrected by the
// "Do queued copies" lines it announces its batches with.
func NewFileTracker(start Direction) *FileTracker {
	return &FileTracker{way: start, pending: map[string]bool{}}
}

// Feed reads one log record and returns the event it describes, if any.
func (t *FileTracker) Feed(r LogRecord) (FileEvent, bool) {
	if r.Level != "info" && r.Level != "notice" {
		return FileEvent{}, false
	}
	msg := ansi.ReplaceAllString(r.Msg, "")
	if d, ok := BisyncDirection(msg); ok {
		t.way = d
		return FileEvent{}, false
	}
	if r.Object == "" {
		return FileEvent{}, false
	}
	ev := FileEvent{Path: r.Object, Way: t.way}
	switch {
	case strings.HasPrefix(msg, "Copied (new)"), strings.HasPrefix(msg, "Copied (server-side copy)"):
		ev.Op = FileNew
		if t.pending[r.Object] {
			ev.Op = FileModified // the old version was moved to the backup folder first
		}
	case strings.HasPrefix(msg, "Copied (replaced existing)"):
		ev.Op = FileModified
	case strings.HasPrefix(msg, "Moved (server-side)"):
		t.pending[r.Object] = true
		return FileEvent{}, false
	case strings.HasPrefix(msg, "Moved into backup dir"), msg == "Deleted":
		ev.Op = FileDeleted
	case strings.HasPrefix(msg, "Made directory"):
		ev.Op, ev.Dir = FileFolder, true
	default:
		return FileEvent{}, false
	}
	delete(t.pending, r.Object)
	return ev, true
}
