// Package tray decides what the notification-area icon and menu say, from the
// daemon's event stream. The decisions live here, separate from the toolkit,
// so they can be tested.
package tray

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/daemon"
)

// Icon names a status icon (assets/status/<theme>/<name>.svg).
type Icon string

const (
	IconIdle        Icon = "idle"
	IconUploading   Icon = "uploading"
	IconDownloading Icon = "downloading"
	IconSyncing     Icon = "syncing"
	IconSynced      Icon = "synced"
	IconFailed      Icon = "failed"
	IconPaused      Icon = "paused"
	// IconOffline is the amber icon for "the daemon is not running". Only a
	// client can know that, so it never comes from an event.
	IconOffline Icon = "offline"
)

// Model is everything the tray shows, rebuilt from the daemon's events.
type Model struct {
	jobs   map[string]daemon.Event
	update string
	// Offline is set while no daemon is listening.
	Offline bool
}

func NewModel() *Model { return &Model{jobs: map[string]daemon.Event{}} }

// Apply records one event. A daemon-level event (empty job) carries the update
// notice; anything else is a job's new state.
func (m *Model) Apply(ev daemon.Event) {
	m.Offline = false
	if ev.Job == "" {
		m.update = ev.Update
		return
	}
	m.jobs[ev.Job] = ev
}

// SetOffline marks the daemon as gone. The job states are dropped: they were
// the daemon's, and showing them would be showing stale news.
func (m *Model) SetOffline() {
	m.Offline = true
	m.jobs = map[string]daemon.Event{}
}

// Update is the newest release the daemon has found, or "".
func (m *Model) Update() string { return m.update }

// Jobs lists the jobs in name order.
func (m *Model) Jobs() []daemon.Event {
	out := make([]daemon.Event, 0, len(m.jobs))
	for _, ev := range m.jobs {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Job < out[j].Job })
	return out
}

// Icon is the one icon that summarises every job. Trouble outranks activity,
// which outranks rest: a failed or paused job must not hide behind a sync that
// is working on another.
func (m *Model) Icon() Icon {
	if m.Offline {
		return IconOffline
	}
	var failed, paused, up, down, both, waiting, synced bool
	for _, ev := range m.jobs {
		switch ev.State {
		case daemon.StateFailed:
			failed = true
		case daemon.StatePaused:
			paused = true
		case daemon.StateUploading:
			up = true
		case daemon.StateDownloading:
			down = true
		case daemon.StateSyncing:
			both = true
		case daemon.StateWaiting:
			waiting = true
		case daemon.StateSynced:
			synced = true
		}
	}
	switch {
	case failed:
		return IconFailed
	case paused:
		return IconPaused
	case both, up && down:
		return IconSyncing
	case up:
		return IconUploading
	case down:
		return IconDownloading
	case waiting:
		return IconIdle // waiting on purpose: the idle icon, the text says why
	case synced:
		return IconSynced
	}
	return IconIdle
}

// Tooltip is the hover text.
func (m *Model) Tooltip() string {
	var b strings.Builder
	b.WriteString("FoxCloud")
	switch {
	case m.Offline:
		b.WriteString(": service not running")
	default:
		for _, ev := range m.Jobs() {
			fmt.Fprintf(&b, "%s%s: %s", "\n", ev.Job, describe(ev))
		}
	}
	if m.update != "" {
		fmt.Fprintf(&b, "%sUpdate available: %s", "\n", m.update)
	}
	return b.String()
}

// JobLine is a job's menu text.
func JobLine(ev daemon.Event) string { return ev.Job + ": " + describe(ev) }

func describe(ev daemon.Event) string {
	s := string(ev.State)
	switch ev.State {
	case daemon.StateWaiting:
		s = "waiting for the system to settle"
		if ev.Until != nil {
			s += " (until " + ev.Until.Local().Format(time.TimeOnly) + ")"
		}
		return s
	case daemon.StateFailed, daemon.StatePaused:
		if ev.Message != "" {
			s += ": " + ev.Message
		}
	}
	return s
}

// UpdateLine is the menu text for the update notice.
func UpdateLine(tag string) string {
	return "Update available: " + tag + " (run foxcloud update)"
}
