package rclone

import "testing"

func TestParseMountRecord(t *testing.T) {
	// Lines taken from a real `rclone mount -v` run (rclone 1.75.1).
	tests := []struct {
		name string
		rec  LogRecord
		want MountChange
		ok   bool
	}{
		{"new", LogRecord{Level: "info", Object: "base/new.txt", Msg: "Copied (new)"},
			MountChange{Kind: MountUpload, Path: "base/new.txt"}, true},
		{"replaced", LogRecord{Level: "info", Object: "base/new.txt", Msg: "Copied (replaced existing)"},
			MountChange{Kind: MountUpload, Path: "base/new.txt"}, true},
		{"rename", LogRecord{Level: "info", Object: "base/new.txt", Msg: "Moved (server-side) to: base/renamed.txt"},
			MountChange{Kind: MountMove, Path: "base/new.txt", To: "base/renamed.txt"}, true},
		{"queued", LogRecord{Level: "info", Object: "base/new.txt", Msg: "vfs cache: queuing for upload in 2s"}, MountChange{}, false},
		{"cache delete", LogRecord{Level: "info", Object: "base/new.txt", Msg: "vfs cache: removed cache file as file deleted"}, MountChange{}, false},
		{"debug", LogRecord{Level: "debug", Object: "base/new.txt", Msg: "Copied (new)"}, MountChange{}, false},
		{"no file", LogRecord{Level: "info", Msg: "Copied (new)"}, MountChange{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseMountRecord(tt.rec)
		if ok != tt.ok || got != tt.want {
			t.Errorf("%s: got %+v, %v; want %+v, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}
