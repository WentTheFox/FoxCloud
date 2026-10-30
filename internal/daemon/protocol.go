// Package daemon runs FoxCloud's sync jobs on their own and tells interested
// clients (the tray, the Plasma widget, the CLI) what each job is doing.
package daemon

import (
	"path/filepath"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/state"
)

// State is what a job is doing right now. It maps one to one onto the status
// icons. "Daemon not running" is deliberately not a State: only a client can
// know that, because the daemon is the thing that is missing.
type State string

const (
	StateWaiting     State = "waiting" // holding off after start-up; shown with the idle icon
	StateIdle        State = "idle"
	StateUploading   State = "uploading"
	StateDownloading State = "downloading"
	StateSyncing     State = "syncing" // both directions
	StateSynced      State = "synced"
	StateFailed      State = "failed"
	StatePaused      State = "paused"
)

// Event is the current state of one job. The daemon sends one whenever a
// job's state changes, so clients never have to poll.
type Event struct {
	Job     string    `json:"job"`
	State   State     `json:"state"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
	// Until is when a "waiting" hold ends.
	Until *time.Time `json:"until,omitempty"`
	// Resync marks a full bisync resync: there is no progress estimate for one.
	Resync bool `json:"resync,omitempty"`
}

// Request is one line of JSON from a client.
type Request struct {
	Cmd string `json:"cmd"` // status, sync-now, stop, subscribe
	Job string `json:"job,omitempty"`
}

// Response answers a Request. For "subscribe" it is followed by a stream of
// Event lines.
type Response struct {
	OK     bool    `json:"ok"`
	Error  string  `json:"error,omitempty"`
	Events []Event `json:"events,omitempty"`
}

// SocketPath is where the daemon listens.
func SocketPath() (string, error) {
	dir, err := state.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "foxcloud.sock"), nil
}
