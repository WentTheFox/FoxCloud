package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/delta"
	"github.com/WentTheFox/FoxCloud/internal/engine"
	"github.com/WentTheFox/FoxCloud/internal/filter"
	"github.com/WentTheFox/FoxCloud/internal/rclone"
	"github.com/WentTheFox/FoxCloud/internal/watch"
)

// Runner performs one sync pass of a job. onResync is called if the pass turns
// out to be a full resync, and onDirection as a bisync pass switches between
// uploading and downloading.
type Runner func(ctx context.Context, job string, onResync func(), onDirection func(rclone.Direction)) error

// Mounter runs a mount job until ctx is cancelled or the mount dies, calling
// onReady once the mount is usable.
type Mounter func(ctx context.Context, job string, onReady func()) error

// MountRetry is how long a failed mount waits before it is tried again.
var MountRetry = time.Minute

// Daemon schedules the jobs and publishes their state.
type Daemon struct {
	run Runner
	// Logf receives the daemon's own diagnostics; the default discards them.
	Logf func(format string, args ...any)
	// WatchQuiet is how long a watched folder must stay silent before its
	// changes are uploaded, and WatchMaxWait caps the wait under constant activity.
	WatchQuiet   time.Duration
	WatchMaxWait time.Duration
	// Apply carries a few file changes over to the remote without a full pass.
	// Without it a change just triggers a pass.
	Apply Applier
	// JournalWrite records applied changes in the shared journal, and Fetch
	// downloads files named by other machines' records. Without them the
	// journal is not used.
	JournalWrite JournalWriter
	Fetch        Fetcher
	journalState
	// OnBusy is told when the daemon starts or stops having sync work in flight
	// (a pass running, or changes noticed and not yet uploaded). The shutdown
	// guard listens to it.
	OnBusy func(busy bool)
	// Mount runs mount jobs. Without it they are left alone.
	Mount  Mounter
	mounts []config.Job
	// gate is held for reading by every running pass and for writing by
	// Exclusive: a writer waits for the passes to finish and keeps new ones out.
	gate sync.RWMutex
	// jobMu serialises a job's passes with its file-by-file uploads.
	jobMu map[string]*sync.Mutex
	// busy counts the work in flight: running passes plus jobs with changes
	// noticed but not yet handled (busyJobs).
	busyMu       sync.Mutex
	running      int
	busyJobs     map[string]bool
	startupDelay time.Duration
	jobs         []config.Job

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
		run:          run,
		Logf:         func(string, ...any) {},
		WatchQuiet:   DefaultWatchQuiet,
		WatchMaxWait: DefaultWatchMaxWait,
		jobMu:        map[string]*sync.Mutex{},
		journalState: journalState{
			journalCh: make(chan journalWork, journalQueue),
			incoming:  map[string]*incoming{},
			firstPass: map[string]chan struct{}{},
		},
		busyJobs:     map[string]bool{},
		startupDelay: startupDelay,
		state:        map[string]Event{},
		subs:         map[chan Event]struct{}{},
		trig:         map[string]chan struct{}{},
		cancel:       map[string]context.CancelFunc{},
	}
	for _, j := range jobs {
		if j.Role == config.RoleMount {
			d.mounts = append(d.mounts, j)
			continue
		}
		d.jobs = append(d.jobs, j)
		d.jobMu[j.Name] = &sync.Mutex{}
		d.firstPass[j.Name] = make(chan struct{})
		d.trig[j.Name] = make(chan struct{}, 1)
	}
	return d
}

// Run drives every job until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) {
	d.ctxMu.Lock()
	d.ctx = ctx
	d.ctxMu.Unlock()
	if d.JournalWrite != nil {
		go d.journalWorker(ctx)
	}
	var wg sync.WaitGroup
	for _, j := range d.jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.loop(ctx, j)
		}()
	}
	if d.Mount != nil {
		for _, j := range d.mounts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				d.mountLoop(ctx, j)
			}()
		}
	}
	wg.Wait()
}

// mountLoop keeps one mount job's mount up: it runs, and if the mount ends
// without being asked to, says why and tries again after MountRetry. A pause
// (something a person has to fix) is not retried until resumed, which the
// next attempt notices.
func (d *Daemon) mountLoop(ctx context.Context, j config.Job) {
	for {
		d.emit(Event{Job: j.Name, State: StateIdle, Message: "mounting"})
		err := d.Mount(ctx, j.Name, func() {
			d.emit(Event{Job: j.Name, State: StateMounted, Message: "mounted at " + j.MountPoint})
		})
		if ctx.Err() != nil {
			return
		}
		var se *engine.SyncError
		switch {
		case errors.Is(err, engine.ErrPaused):
			d.emit(Event{Job: j.Name, State: StatePaused, Message: err.Error()})
		case errors.As(err, &se) && se.Pause:
			d.emit(Event{Job: j.Name, State: StatePaused, Message: se.Message})
		default:
			d.emit(Event{Job: j.Name, State: StateFailed, Message: err.Error()})
		}
		t := time.NewTimer(MountRetry)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
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
			close(d.firstPass[j.Name])
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

// Defaults for watching a folder.
const (
	DefaultWatchQuiet   = 3 * time.Second
	DefaultWatchMaxWait = 30 * time.Second
)

// Applier carries file changes over to the remote; engine.Engine.Apply is one.
// It returns an error (engine.ErrNeedsFullPass among them) when a full pass
// should run instead.
type Applier func(ctx context.Context, job string, ops delta.Ops) error

// setBusy and passBusy keep the in-flight count and tell OnBusy when it
// crosses zero in either direction.
func (d *Daemon) busyLocked() bool { return d.running > 0 || len(d.busyJobs) > 0 }

func (d *Daemon) change(fn func()) {
	d.busyMu.Lock()
	was := d.busyLocked()
	fn()
	now := d.busyLocked()
	d.busyMu.Unlock()
	if was != now && d.OnBusy != nil {
		d.OnBusy(now)
	}
}

// Busy reports whether sync work is in flight right now.
func (d *Daemon) Busy() bool {
	d.busyMu.Lock()
	defer d.busyMu.Unlock()
	return d.busyLocked()
}

// startWatching makes changes under a push or bisync job's folder reach the
// remote soon after they happen: the folder is watched, and each burst of
// changes is uploaded file by file (see Applier), with a full pass only when
// that cannot be trusted. Pull jobs are not watched: they write into their own
// folder and would trigger themselves.
func (d *Daemon) startWatching(ctx context.Context, j config.Job) {
	if !j.Watch || (j.Role != config.RolePush && j.Role != config.RoleBisync) {
		return
	}
	var ix *delta.Index
	opts := watch.Options{
		Quiet:   d.WatchQuiet,
		MaxWait: d.WatchMaxWait,
		// The tree is read once the watches are in place, so nothing slips in
		// between the two.
		OnReady: func() {
			f, err := filter.Load(j.FilterFile)
			if err != nil {
				d.Logf("job %s: filter file: %v; watching without a filter", j.Name, err)
				f = &filter.Matcher{}
			}
			if ix, err = delta.Scan(j.LocalPath, f); err != nil {
				d.Logf("job %s: scanning %s: %v", j.Name, j.LocalPath, err)
			}
		},
		OnActive: func() { d.change(func() { d.busyJobs[j.Name] = true }) },
		OnIdle:   func() { d.change(func() { delete(d.busyJobs, j.Name) }) },
	}
	go func() {
		err := watch.Batches(ctx, j.LocalPath, opts, func(b watch.Batch) { d.handleBatch(ctx, j, ix, b) }, d.Logf)
		d.change(func() { delete(d.busyJobs, j.Name) })
		if err != nil && ctx.Err() == nil {
			d.Logf("job %s: file watching is off, the interval still applies: %v", j.Name, err)
		}
	}()
}

// handleBatch uploads one burst of changes, or asks for a full pass when it
// cannot do that safely.
func (d *Daemon) handleBatch(ctx context.Context, j config.Job, ix *delta.Index, b watch.Batch) {
	if ix == nil || d.Apply == nil {
		d.SyncNow(j.Name)
		return
	}
	ops := ix.Update(b.Paths, b.Overflow)
	if ops.Empty() {
		return
	}
	if ops.Rescan {
		d.SyncNow(j.Name)
		return
	}

	d.gate.RLock()
	defer d.gate.RUnlock()
	d.jobMu[j.Name].Lock()
	defer d.jobMu[j.Name].Unlock()
	if ctx.Err() != nil {
		return
	}

	d.mu.Lock()
	prev := d.state[j.Name]
	d.mu.Unlock()
	d.emit(Event{Job: j.Name, State: StateUploading, Message: summary(ops)})
	err := d.Apply(ctx, j.Name, ops)
	switch {
	case ctx.Err() != nil:
		return
	case err != nil:
		// Whatever it was (a full pass was asked for, the network, a pause), a
		// normal pass reports it properly, so hand over to one.
		d.Logf("job %s: uploading %s file by file did not work (%v); running a full pass", j.Name, summary(ops), err)
		d.emit(prev)
		d.SyncNow(j.Name)
	case prev.State == StateFailed || prev.State == StatePaused:
		d.emit(prev) // a good upload does not clear an earlier failure
		d.enqueueJournal(j, ops)
	default:
		d.emit(Event{Job: j.Name, State: StateSynced, Message: "uploaded " + summary(ops)})
		d.enqueueJournal(j, ops)
	}
}

func summary(o delta.Ops) string {
	n := len(o.Uploads) + len(o.Moves) + o.Deletes
	if n == 0 {
		n = len(o.Mkdirs)
	}
	if n == 1 {
		return "1 change"
	}
	return fmt.Sprintf("%d changes", n)
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
	d.gate.RLock()
	defer d.gate.RUnlock()
	d.jobMu[j.Name].Lock()
	defer d.jobMu[j.Name].Unlock()
	// A pass that can upload keeps the machine from shutting down; a pull
	// only downloads, and losing it costs nothing.
	if j.Role != config.RolePull {
		d.change(func() { d.running++ })
		defer d.change(func() { d.running-- })
	}
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

// Exclusive runs fn while no sync pass is running and none can start: it waits
// for the running ones to finish, which is what installing an update needs. It
// does not touch mounts. fn should be short.
func (d *Daemon) Exclusive(fn func() error) error {
	d.gate.Lock()
	defer d.gate.Unlock()
	return fn()
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
