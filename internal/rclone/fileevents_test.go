package rclone

import (
	"reflect"
	"testing"
)

func feed(t *FileTracker, lines ...[3]string) []FileEvent {
	var out []FileEvent
	for _, l := range lines {
		if ev, ok := t.Feed(LogRecord{Level: l[0], Object: l[1], Msg: l[2]}); ok {
			out = append(out, ev)
		}
	}
	return out
}

// The lines below are what rclone 1.75 printed for a push without a backup dir.
func TestPushPassWithoutBackupDir(t *testing.T) {
	got := feed(NewFileTracker(Upload),
		[3]string{"info", "sub", "Set directory modification time (using SetModTime)"},
		[3]string{"info", "added.txt", "Copied (new)"},
		[3]string{"info", "mod.txt", "Copied (replaced existing)"},
		[3]string{"info", "del.txt", "Deleted"},
		[3]string{"info", "newdir", "Made directory with metadata (mtime=2026-10-31T14:23:36+01:00)"},
	)
	want := []FileEvent{
		{FileNew, "added.txt", false, Upload},
		{FileModified, "mod.txt", false, Upload},
		{FileDeleted, "del.txt", false, Upload},
		{FileFolder, "newdir", true, Upload},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// With a backup dir the old file is moved aside first.
func TestBackupDirMakesAReplacementAModificationAndADeletionADelete(t *testing.T) {
	got := feed(NewFileTracker(Download),
		[3]string{"info", "mod.txt", "Moved (server-side) to: mod.txt.bak"},
		[3]string{"info", "added.txt", "Copied (new)"},
		[3]string{"info", "mod.txt", "Copied (new)"},
		[3]string{"info", "del.txt", "Moved (server-side) to: del.txt.bak"},
		[3]string{"info", "del.txt", "Moved into backup dir"},
	)
	want := []FileEvent{
		{FileNew, "added.txt", false, Download},
		{FileModified, "mod.txt", false, Download},
		{FileDeleted, "del.txt", false, Download},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// bisync announces which side each batch goes to; deletes follow the batch.
func TestBisyncFollowsItsAnnouncedDirection(t *testing.T) {
	got := feed(NewFileTracker(Upload),
		[3]string{"info", "", "- Path2    Do queued copies to                - Path1"},
		[3]string{"info", "R.txt", "Deleted"},
		[3]string{"info", "", "- Path1    Do queued copies to                - Path2"},
		[3]string{"info", "L2.txt", "Copied (new)"},
		[3]string{"info", "L.txt", "Deleted"},
	)
	want := []FileEvent{
		{FileDeleted, "R.txt", false, Upload},
		{FileNew, "L2.txt", false, Download},
		{FileDeleted, "L.txt", false, Download},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestOnlyInfoAndNoticeLinesCount(t *testing.T) {
	got := feed(NewFileTracker(Upload),
		[3]string{"error", "a.txt", "Copied (new)"},
		[3]string{"debug", "b.txt", "Copied (new)"},
		[3]string{"info", "", "Copied (new)"},
	)
	if len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}
