package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/engine"
	"github.com/WentTheFox/FoxCloud/internal/rclone"
	"github.com/WentTheFox/FoxCloud/internal/watch"
)

// Runner performs one sync pass of a job. onResync is called if the pass turns
// out to be a full resync, and onDirection as a bisync pass switches between
// uploading and downloading.
type Runner func(ctx context.Context, job string, onResync func(), onDirection func(rclone.Direction)) error

// Daemon schedules the jobs and publishes their state.
type Daemon struct {
	run Runner
	// Logf receives the daemon's own diagnostics; the default discards them.
	Logf          func(format string, args ...any)
	WatchDebounce time.Duration
	startupDelay  time.Duration
	jobs          []config.Job

	mu     sync.Mutex
	state  map[string]Event
	subs   map[chan Event]struct{}
	trig   map[string]chan struct{}
	cancel map[string]context.CancelFunc
}

// New creates a daemon for the jobs it can run (mount jobs are not synced).
// startupDelay is the hold before each job's first pass.
func New(jobs []config.Job, run Runner, startupDelay time.Duration) *Daemon {
	d := &Daemon{
		run:           run,
		Logf:          func(string, ...any) {},
		WatchDebounce: DefaultWatchDebounce,
		startupDelay:  startupDelay,
		state:         map[string]Event{},
		subs:          map[chan Event]struct{}{},
		trig:          map[string]chan struct{}{},
		cancel:        map[string]context.CancelFunc{},
	}
	for _, j := range jobs {
		if j.Role == config.RoleMount {
			continue
		}
		d.jobs = append(d.jobs, j)
		d.trig[j.Name] = make(chan struct{}, 1)
	}
	return d
}

// Run drives every job until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, j := range d.jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.loop(ctx, j)
		}()
	}
	wg.Wait()
}

// loop is one job's lifetime: hold, then pass, wait, pass, wait... Waiting is
// on a timer or a trigger, never a poll.
func (d *Daemon) loop(ctx context.Context, j config.Job) {
	if d.startupDelay > 0 {
		until := time.Now().Add(d.startupDelay)
		d.emit(Event{Job: j.Name, State: StateWaiting, Until: &until,
			Message: "waiting for the system to settle"})
		hold := time.NewTimer(d.startupDelay)
		select {
		case <-ctx.Done():
			hold.Stop()
			return
		case <-hold.C:
		case <-d.trig[j.Name]: // an explicit sync-now skips the hold
			hold.Stop()
		}
	}
	for first := true; ; first = false {
		d.pass(ctx, j)
		if first {
			// After the start-up hold and a first pass (which creates the
			// folder), so changes during the hold cannot skip it.
			d.startWatching(ctx, j)
		}

		var tick <-chan time.Time
		var timer *time.Timer
		if iv := j.IntervalDuration(); iv > 0 {
			timer = time.NewTimer(iv)
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-tick:
		case <-d.trig[j.Name]:
			if timer != nil {
				timer.Stop()
			}
		}
	}
}

// DefaultWatchDebounce is how long a watched folder must stay quiet before the
// change triggers a pass.
const DefaultWatchDebounce = 30 * time.Second

// startWatching makes changes under a push job's folder trigger a pass. Only
// push jobs: pull and bisync write into their own folder, so they would
// trigger themselves.
func (d *Daemon) startWatching(ctx context.Context, j config.Job) {
	if !j.Watch || j.Role != config.RolePush {
		return
	}
	go func() {
		err := watch.Run(ctx, j.LocalPath, d.WatchDebounce, func() { d.SyncNow(j.Name) }, d.Logf)
		if err != nil && ctx.Err() == nil {
			d.Logf("job %s: file watching is off, the interval still applies: %v", j.Name, err)
		}
	}()
}

// activeState is what a running pass of this role is doing.
func activeState(role string) State {
	switch role {
	case config.RolePush:
		return StateUploading
	case config.RolePull:
		return StateDownloading
	default:
		return StateSyncing
	}
}

func (d *Daemon) pass(parent context.Context, j config.Job) {
	ctx, cancel := context.WithCancel(parent)
	d.mu.Lock()
	d.cancel[j.Name] = cancel
	d.mu.Unlock()
	defer func() {
		cancel()
		d.mu.Lock()
		delete(d.cancel, j.Name)
		d.mu.Unlock()
	}()

	active := activeState(j.Role)
	d.emit(Event{Job: j.Name, State: active})
	err := d.run(ctx, j.Name, func() {
		d.emit(Event{Job: j.Name, State: active, Resync: true,
			Message: "full resync: there is no progress estimate for this run"})
	}, func(dir rclone.Direction) {
		// Only bisync reports a direction; push and pull keep their fixed one.
		if j.Role != config.RoleBisync {
			return
		}
		next := StateUploading
		if dir == rclone.Download {
			next = StateDownloading
		}
		d.emit(Event{Job: j.Name, State: next})
	})

	var se *engine.SyncError
	switch {
	case err == nil:
		d.emit(Event{Job: j.Name, State: StateSynced})
	case parent.Err() != nil:
		// shutting down: leave the last state as it was
	case ctx.Err() != nil:
		d.emit(Event{Job: j.Name, State: StateIdle, Message: "stopped"})
	case errors.Is(err, engine.ErrPaused):
		d.emit(Event{Job: j.Name, State: StatePaused, Message: err.Error()})
	case errors.As(err, &se) && se.Pause:
		d.emit(Event{Job: j.Name, State: StatePaused, Message: se.Message})
	default:
		d.emit(Event{Job: j.Name, State: StateFailed, Message: err.Error()})
	}
}

// emit records the job's new state and tells every subscriber. A subscriber
// that cannot keep up misses events instead of holding the daemon back; it is
// resynchronised by the snapshot it takes when it next subscribes.
func (d *Daemon) emit(ev Event) {
	ev.At = time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state[ev.Job] = ev
	for ch := range d.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// SetUpdate records that a newer release exists and tells subscribers, so a
// tray or widget can say so. It is a daemon-level event: its Job is empty.
func (d *Daemon) SetUpdate(tag string) {
	d.mu.Lock()
	old := d.state[""].Update
	d.mu.Unlock()
	if old == tag {
		return
	}
	d.emit(Event{Update: tag})
}

// Snapshot returns every job's current state, in job-name order.
func (d *Daemon) Snapshot() []Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotLocked()
}

func (d *Daemon) snapshotLocked() []Event {
	out := make([]Event, 0, len(d.state))
	for _, ev := range d.state {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Job < out[j].Job })
	return out
}

// Subscribe returns the current state plus a channel of later changes. Call
// the returned function to stop listening.
func (d *Daemon) Subscribe() (snapshot []Event, events <-chan Event, cancel func()) {
	ch := make(chan Event, 64)
	d.mu.Lock()
	d.subs[ch] = struct{}{}
	snapshot = d.snapshotLocked()
	d.mu.Unlock()
	return snapshot, ch, func() {
		d.mu.Lock()
		delete(d.subs, ch)
		d.mu.Unlock()
	}
}

func (d *Daemon) knows(job string) bool {
	_, ok := d.trig[job]
	return ok
}

// SyncNow starts a pass for the job as soon as possible, skipping any
// start-up hold or interval wait. If a pass is already running, one more is
// queued behind it.
func (d *Daemon) SyncNow(job string) error {
	if !d.knows(job) {
		return fmt.Errorf("no syncable job named %q", job)
	}
	select {
	case d.trig[job] <- struct{}{}:
	default: // one is already queued
	}
	return nil
}

// Stop cancels the job's running pass. It does nothing if none is running.
func (d *Daemon) Stop(job string) error {
	if !d.knows(job) {
		return fmt.Errorf("no syncable job named %q", job)
	}
	d.mu.Lock()
	cancel := d.cancel[job]
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}
