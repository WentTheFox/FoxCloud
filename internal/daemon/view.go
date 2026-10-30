package daemon

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/fsnotify/fsnotify"
)

// View is everything a status display needs in one JSON document: whether the
// daemon is up, each job's state, and the update notice. Rev changes whenever
// anything in it does, so a client can ask to be told about the next change
// without polling. It is what `foxcloud status-json` prints for the Plasma
// widget.
type View struct {
	Running bool    `json:"running"`
	Rev     string  `json:"rev"`
	Update  string  `json:"update,omitempty"`
	Jobs    []Event `json:"jobs"`
}

// OfflineRev is the Rev of a view with no daemon behind it.
const OfflineRev = "offline"

// OfflineView is what a client sees while the daemon is not running.
func OfflineView() View { return View{Rev: OfflineRev, Jobs: []Event{}} }

// BuildView turns the daemon's events into a View. A daemon-level event (empty
// job) carries the update notice.
func BuildView(events []Event) View {
	v := View{Running: true, Jobs: []Event{}}
	for _, ev := range events {
		if ev.Job == "" {
			v.Update = ev.Update
			continue
		}
		v.Jobs = append(v.Jobs, ev)
	}
	sort.Slice(v.Jobs, func(i, j int) bool { return v.Jobs[i].Job < v.Jobs[j].Job })
	b, _ := json.Marshal(struct {
		Update string
		Jobs   []Event
	}{v.Update, v.Jobs})
	sum := sha1.Sum(b)
	v.Rev = hex.EncodeToString(sum[:8])
	return v
}

// CurrentView asks the running daemon for its state. No daemon is a normal
// answer, not an error: it is the offline view.
func CurrentView(path string) (View, error) {
	resp, err := Do(path, Request{Cmd: "status"})
	if errors.Is(err, ErrNotRunning) {
		return OfflineView(), nil
	}
	if err != nil {
		return View{}, err
	}
	return BuildView(resp.Events), nil
}

// WaitView returns the first view whose Rev differs from known, blocking until
// there is one: on the daemon's event stream while it runs, and on the socket
// file appearing while it does not. Nothing polls. A client that passes the
// Rev of what it last showed cannot miss a change, because the comparison
// happens against the current state when the wait begins.
func WaitView(ctx context.Context, path, known string) (View, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return View{}, err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return View{}, err
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		return View{}, err
	}

	for {
		var result *View
		state := map[string]Event{}
		current := func() View {
			evs := make([]Event, 0, len(state))
			for _, ev := range state {
				evs = append(evs, ev)
			}
			return BuildView(evs)
		}
		watchCtx, cancel := context.WithCancel(ctx)
		check := func() {
			if v := current(); v.Rev != known && result == nil {
				result = &v
				cancel()
			}
		}
		err := WatchSnapshot(watchCtx, path, func(evs []Event) {
			for _, ev := range evs {
				state[ev.Job] = ev
			}
			check()
		}, func(ev Event) {
			state[ev.Job] = ev
			check()
		})
		cancel()
		if result != nil {
			return *result, nil
		}
		if ctx.Err() != nil {
			return View{}, ctx.Err()
		}
		if !errors.Is(err, ErrNotRunning) {
			return View{}, err
		}
		// The daemon is not running, or has just gone.
		if known != OfflineRev {
			return OfflineView(), nil
		}
		if err := waitForSocket(ctx, w, path); err != nil {
			return View{}, err
		}
	}
}
