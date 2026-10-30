package tray

import (
	"strings"
	"testing"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/daemon"
)

func ev(job string, s daemon.State) daemon.Event { return daemon.Event{Job: job, State: s} }

func TestIconPriority(t *testing.T) {
	cases := []struct {
		name   string
		events []daemon.Event
		want   Icon
	}{
		{"nothing yet", nil, IconIdle},
		{"waiting shows idle", []daemon.Event{ev("a", daemon.StateWaiting)}, IconIdle},
		{"synced", []daemon.Event{ev("a", daemon.StateSynced)}, IconSynced},
		{"uploading", []daemon.Event{ev("a", daemon.StateUploading)}, IconUploading},
		{"downloading", []daemon.Event{ev("a", daemon.StateDownloading)}, IconDownloading},
		{"two directions at once", []daemon.Event{ev("a", daemon.StateUploading), ev("b", daemon.StateDownloading)}, IconSyncing},
		{"bisync both", []daemon.Event{ev("a", daemon.StateSyncing)}, IconSyncing},
		{"paused beats activity", []daemon.Event{ev("a", daemon.StateUploading), ev("b", daemon.StatePaused)}, IconPaused},
		{"failed beats everything", []daemon.Event{ev("a", daemon.StatePaused), ev("b", daemon.StateFailed), ev("c", daemon.StateSyncing)}, IconFailed},
	}
	for _, c := range cases {
		m := NewModel()
		for _, e := range c.events {
			m.Apply(e)
		}
		if got := m.Icon(); got != c.want {
			t.Errorf("%s: icon = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestOfflineOverridesAndClearsJobs(t *testing.T) {
	m := NewModel()
	m.Apply(ev("a", daemon.StateFailed))
	m.SetOffline()
	if m.Icon() != IconOffline || len(m.Jobs()) != 0 {
		t.Errorf("offline: icon %s, %d jobs", m.Icon(), len(m.Jobs()))
	}
	if !strings.Contains(m.Tooltip(), "not running") {
		t.Errorf("tooltip = %q", m.Tooltip())
	}
	m.Apply(ev("a", daemon.StateIdle)) // the daemon came back
	if m.Offline || m.Icon() != IconIdle {
		t.Error("an event from the daemon must end the offline state")
	}
}

func TestUpdateNoticeIsKeptApartFromJobs(t *testing.T) {
	m := NewModel()
	m.Apply(ev("a", daemon.StateSynced))
	m.Apply(daemon.Event{Update: "v1.2.3"})
	if m.Update() != "v1.2.3" || len(m.Jobs()) != 1 {
		t.Errorf("update %q, jobs %d", m.Update(), len(m.Jobs()))
	}
	if !strings.Contains(m.Tooltip(), "Update available: v1.2.3") {
		t.Errorf("tooltip = %q", m.Tooltip())
	}
	if !strings.Contains(UpdateLine("v1.2.3"), "foxcloud update") {
		t.Error("the menu line should say how to update")
	}
}

func TestDescribeWaitingAndFailures(t *testing.T) {
	until := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	w := daemon.Event{Job: "a", State: daemon.StateWaiting, Until: &until}
	if got := JobLine(w); !strings.Contains(got, "settle") || !strings.Contains(got, "until") {
		t.Errorf("waiting line = %q", got)
	}
	f := daemon.Event{Job: "a", State: daemon.StateFailed, Message: "network down"}
	if got := JobLine(f); got != "a: failed: network down" {
		t.Errorf("failed line = %q", got)
	}
}
