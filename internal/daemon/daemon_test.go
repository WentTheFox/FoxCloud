package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/classify"
	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/delta"
	"github.com/WentTheFox/FoxCloud/internal/engine"
	"github.com/WentTheFox/FoxCloud/internal/history"
	"github.com/WentTheFox/FoxCloud/internal/journal"
	"github.com/WentTheFox/FoxCloud/internal/rclone"
)

func job(role, interval string) []config.Job {
	return []config.Job{{Name: "a", Role: role, Interval: interval}}
}

// start runs the daemon in the background and returns a way to wait for states.
func start(t *testing.T, d *Daemon) (await func(State) Event) {
	t.Helper()
	// Subscribe first: a job that finishes at once would otherwise be missed.
	_, events, unsub := d.Subscribe()
	t.Cleanup(unsub)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return func(want State) Event {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case ev := <-events:
				if ev.State == want {
					return ev
				}
			case <-deadline:
				t.Fatalf("timed out waiting for state %q", want)
			}
		}
	}
}

func ok(context.Context, string, func(), func(rclone.Direction)) error { return nil }

func TestHoldsBeforeTheFirstPass(t *testing.T) {
	var ranAt atomic.Int64
	began := time.Now()
	d := New(job(config.RolePush, ""), func(context.Context, string, func(), func(rclone.Direction)) error {
		ranAt.Store(time.Now().UnixNano())
		return nil
	}, 120*time.Millisecond)
	await := start(t, d)

	if w := d.Snapshot(); len(w) != 0 {
		// the waiting event is emitted by the loop; it may or may not be in yet
		if w[0].State != StateWaiting {
			t.Errorf("state during the hold = %q, want waiting", w[0].State)
		}
	}
	await(StateSynced)
	if waited := time.Duration(ranAt.Load() - began.UnixNano()); waited < 100*time.Millisecond {
		t.Errorf("first pass ran after %v, want it held for about 120ms", waited)
	}
}

func TestWaitingEventSaysWhenTheHoldEnds(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, time.Hour)
	await := start(t, d)
	ev := await(StateWaiting)
	if ev.Until == nil || time.Until(*ev.Until) < 59*time.Minute {
		t.Errorf("Until = %v, want about an hour from now", ev.Until)
	}
}

func TestSyncNowSkipsTheHold(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, time.Hour)
	await := start(t, d)
	await(StateWaiting)
	if err := d.SyncNow("a"); err != nil {
		t.Fatal(err)
	}
	await(StateSynced)
}

func TestIntervalRepeats(t *testing.T) {
	var n atomic.Int32
	d := New(job(config.RolePush, "30ms"), func(context.Context, string, func(), func(rclone.Direction)) error {
		n.Add(1)
		return nil
	}, 0)
	start(t, d)
	deadline := time.Now().Add(3 * time.Second)
	for n.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := n.Load(); got < 3 {
		t.Errorf("only %d passes ran, want at least 3", got)
	}
}

func TestAPassStartsCheckingNotTransferring(t *testing.T) {
	for _, role := range []string{config.RolePush, config.RolePull, config.RoleBisync} {
		release := make(chan struct{})
		d := New(job(role, ""), func(ctx context.Context, _ string, _ func(), _ func(rclone.Direction)) error {
			<-release
			return nil
		}, 0)
		await := start(t, d)
		if ev := await(StateChecking); ev.Message == "" {
			t.Errorf("%s: the checking state should say what it is doing", role)
		}
		close(release)
		await(StateSynced)
	}
}

func TestTransferringStartsWhenTheRunnerReportsIt(t *testing.T) {
	for role, dir := range map[string]rclone.Direction{
		config.RolePush: rclone.Upload, config.RolePull: rclone.Download,
	} {
		want := map[rclone.Direction]State{rclone.Upload: StateUploading, rclone.Download: StateDownloading}[dir]
		reported, release := make(chan struct{}), make(chan struct{})
		d := New(job(role, ""), func(_ context.Context, _ string, _ func(), onDir func(rclone.Direction)) error {
			onDir(dir)
			close(reported)
			<-release
			return nil
		}, 0)
		await := start(t, d)
		await(StateChecking)
		await(want)
		<-reported
		close(release)
		await(StateSynced)
	}
}

func TestAPassWithNothingToTransferNeverSaysUploading(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, 0)
	_, events, unsub := d.Subscribe()
	defer unsub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.State == StateUploading {
				t.Fatal("a pass that moved nothing reported uploading")
			}
			if ev.State == StateSynced {
				return
			}
		case <-deadline:
			t.Fatal("no synced event")
		}
	}
}
func TestFailureStates(t *testing.T) {
	tests := map[string]struct {
		err  error
		want State
		msg  string
	}{
		"plain failure":  {&engine.SyncError{Failure: classify.Failure{Message: "disk on fire"}}, StateFailed, "disk on fire"},
		"pausing error":  {&engine.SyncError{Failure: classify.Failure{Message: "too many", Pause: true}}, StatePaused, "too many"},
		"already paused": {fmt.Errorf("%w: look first", engine.ErrPaused), StatePaused, ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			d := New(job(config.RolePush, ""), func(context.Context, string, func(), func(rclone.Direction)) error { return tt.err }, 0)
			await := start(t, d)
			ev := await(tt.want)
			if tt.msg != "" && ev.Message != tt.msg {
				t.Errorf("message = %q, want %q", ev.Message, tt.msg)
			}
		})
	}
}

func TestStopCancelsTheRunningPass(t *testing.T) {
	started := make(chan struct{})
	d := New(job(config.RolePush, ""), func(ctx context.Context, _ string, _ func(), _ func(rclone.Direction)) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}, 0)
	await := start(t, d)
	<-started
	if err := d.Stop("a"); err != nil {
		t.Fatal(err)
	}
	if ev := await(StateIdle); ev.Message != "stopped" {
		t.Errorf("message = %q, want \"stopped\"", ev.Message)
	}
}

func TestResyncIsFlagged(t *testing.T) {
	d := New(job(config.RoleBisync, ""), func(_ context.Context, _ string, onResync func(), _ func(rclone.Direction)) error {
		onResync()
		return nil
	}, 0)
	await := start(t, d)
	var sawResync bool
	for i := 0; i < 3 && !sawResync; i++ {
		sawResync = await(StateSyncing).Resync
	}
	if !sawResync {
		t.Error("no event carried the resync flag")
	}
}

func TestUnknownJobsAreRejected(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, time.Hour)
	if d.SyncNow("nope") == nil || d.Stop("nope") == nil {
		t.Error("expected errors for an unknown job")
	}
}

func TestMountJobsAreNotScheduled(t *testing.T) {
	d := New([]config.Job{{Name: "m", Role: config.RoleMount}}, ok, 0)
	if d.SyncNow("m") == nil {
		t.Error("a mount job must not be syncable")
	}
}

func TestSlowSubscribersDoNotBlockTheDaemon(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, 0)
	_, _, unsub := d.Subscribe() // never reads
	defer unsub()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			d.emit(Event{Job: "a", State: StateSynced})
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("emit blocked on a subscriber that never reads")
	}
}

func TestBisyncDirectionFollowsRclone(t *testing.T) {
	d := New(job(config.RoleBisync, ""), func(_ context.Context, _ string, _ func(), onDir func(rclone.Direction)) error {
		onDir(rclone.Download)
		onDir(rclone.Upload)
		return nil
	}, 0)
	await := start(t, d)
	await(StateDownloading)
	await(StateUploading)
}

func TestWatchedPushJobSyncsAfterChange(t *testing.T) {
	dir := t.TempDir()
	var passes atomic.Int32
	jobs := []config.Job{{Name: "a", Role: config.RolePush, LocalPath: dir, Watch: true}}
	d := New(jobs, func(context.Context, string, func(), func(rclone.Direction)) error {
		passes.Add(1)
		return nil
	}, 0)
	d.WatchQuiet = 100 * time.Millisecond
	await := start(t, d)
	await(StateSynced)
	time.Sleep(300 * time.Millisecond) // the watch starts after the first pass
	os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	await(StateSynced)
	if passes.Load() < 2 {
		t.Errorf("passes = %d, want a second one after the file changed", passes.Load())
	}
}

func TestUpdateNoticeReachesSubscribersAndSnapshot(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, time.Hour)
	_, events, cancel := d.Subscribe()
	defer cancel()

	d.SetUpdate("v1.2.3")
	d.SetUpdate("v1.2.3") // unchanged: no second notice
	select {
	case ev := <-events:
		if ev.Job != "" || ev.Update != "v1.2.3" {
			t.Errorf("event = %+v, want a daemon-level update notice", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no update event")
	}
	select {
	case ev := <-events:
		t.Errorf("repeated tag produced another event: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	snap, _, cancel2 := d.Subscribe()
	defer cancel2()
	found := false
	for _, ev := range snap {
		found = found || (ev.Job == "" && ev.Update == "v1.2.3")
	}
	if !found {
		t.Error("a later subscriber's snapshot lacks the update notice")
	}
}

func TestMountJobsAreSupervised(t *testing.T) {
	old := MountRetry
	MountRetry = 20 * time.Millisecond
	defer func() { MountRetry = old }()

	var attempts atomic.Int32
	jobs := []config.Job{{Name: "m", Role: config.RoleMount, MountPoint: "X:"}}
	d := New(jobs, ok, 0)
	d.Mount = func(ctx context.Context, _ string, onReady func()) error {
		if attempts.Add(1) == 1 {
			return fmt.Errorf("winfsp missing") // first try fails
		}
		onReady()
		<-ctx.Done() // then the mount stays up
		return nil
	}
	await := start(t, d)
	await(StateFailed)
	ev := await(StateMounted)
	if ev.Message == "" {
		t.Error("the mounted event should say where")
	}
	if attempts.Load() != 2 {
		t.Errorf("attempts = %d, want 2", attempts.Load())
	}
}

func TestMountPauseIsReported(t *testing.T) {
	jobs := []config.Job{{Name: "m", Role: config.RoleMount, MountPoint: "X:"}}
	d := New(jobs, ok, 0)
	d.Mount = func(context.Context, string, func()) error { return engine.ErrPaused }
	await := start(t, d)
	await(StatePaused)
}

func TestExclusiveWaitsForRunningPassesAndBlocksNewOnes(t *testing.T) {
	release := make(chan struct{})
	var running atomic.Int32
	d := New(job(config.RolePush, ""), func(ctx context.Context, _ string, _ func(), _ func(rclone.Direction)) error {
		running.Add(1)
		<-release
		running.Add(-1)
		return nil
	}, 0)
	await := start(t, d)
	await(StateChecking)

	entered := make(chan struct{})
	go d.Exclusive(func() error {
		if running.Load() != 0 {
			t.Error("Exclusive ran while a pass was still running")
		}
		close(entered)
		return nil
	})
	select {
	case <-entered:
		t.Fatal("Exclusive did not wait for the running pass")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Exclusive never ran after the pass finished")
	}
}

func TestPullJobsAreNotWatched(t *testing.T) {
	dir := t.TempDir()
	var passes atomic.Int32
	jobs := []config.Job{{Name: "a", Role: config.RolePull, LocalPath: dir, Watch: true}}
	d := New(jobs, func(context.Context, string, func(), func(rclone.Direction)) error {
		passes.Add(1)
		return nil
	}, 0)
	d.WatchQuiet = 50 * time.Millisecond
	await := start(t, d)
	await(StateSynced)
	os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	time.Sleep(500 * time.Millisecond)
	if passes.Load() != 1 {
		t.Errorf("passes = %d, want 1", passes.Load())
	}
}

// watched starts a push job over a temp folder with an Applier and a runner that
// counts full passes.
func watched(t *testing.T, apply Applier) (d *Daemon, dir string, passes *atomic.Int32, await func(State) Event) {
	t.Helper()
	dir = t.TempDir()
	passes = new(atomic.Int32)
	jobs := []config.Job{{Name: "a", Role: config.RolePush, LocalPath: dir, Watch: true}}
	d = New(jobs, func(context.Context, string, func(), func(rclone.Direction)) error {
		passes.Add(1)
		return nil
	}, 0)
	d.WatchQuiet = 100 * time.Millisecond
	d.Apply = apply
	await = start(t, d)
	await(StateSynced)
	time.Sleep(300 * time.Millisecond) // the watch starts after the first pass
	return d, dir, passes, await
}

func TestWatchedChangesUploadFileByFileWithoutAFullPass(t *testing.T) {
	got := make(chan delta.Ops, 4)
	_, dir, passes, await := watched(t, func(_ context.Context, _ string, ops delta.Ops) error {
		got <- ops
		return nil
	})
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644)
	ev := await(StateSynced)
	select {
	case ops := <-got:
		if len(ops.Uploads) != 1 || ops.Uploads[0] != "new.txt" {
			t.Errorf("ops = %+v", ops)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Apply was not called")
	}
	if ev.Message == "" {
		t.Error("the synced event should say what was uploaded")
	}
	if passes.Load() != 1 {
		t.Errorf("full passes = %d, want only the first", passes.Load())
	}
}

func TestAFailedUploadFallsBackToAFullPass(t *testing.T) {
	_, dir, passes, _ := watched(t, func(context.Context, string, delta.Ops) error {
		return errors.New("network down")
	})
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644)
	deadline := time.Now().Add(5 * time.Second)
	for passes.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if passes.Load() < 2 {
		t.Error("a failed file-by-file upload must trigger a full pass")
	}
}

func TestBusyIsReportedWhileChangesAreInFlight(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var transitions []bool
	d, dir, _, await := watched(t, func(ctx context.Context, _ string, _ delta.Ops) error {
		<-release
		return nil
	})
	d.OnBusy = func(b bool) { mu.Lock(); transitions = append(transitions, b); mu.Unlock() }
	if d.Busy() {
		t.Fatal("nothing is happening yet")
	}
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644)
	deadline := time.Now().Add(3 * time.Second)
	for !d.Busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !d.Busy() {
		t.Fatal("a noticed change must count as work in flight")
	}
	close(release)
	await(StateSynced)
	deadline = time.Now().Add(3 * time.Second)
	for d.Busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if d.Busy() || len(transitions) != 2 || !transitions[0] || transitions[1] {
		t.Errorf("busy = %v, transitions = %v, want [true false]", d.Busy(), transitions)
	}
}

func TestEventsCarryTheConflictCount(t *testing.T) {
	var n atomic.Int64
	n.Store(2)
	d := New(job(config.RolePush, ""), ok, 0)
	d.Conflicts = func(string) int { return int(n.Load()) }
	await := start(t, d)
	if got := await(StateSynced).Conflicts; got != 2 {
		t.Fatalf("conflicts after the pass = %d, want 2", got)
	}

	// A recount that finds a different number re-announces the job's state.
	_, events, unsub := d.Subscribe()
	defer unsub()
	n.Store(0)
	d.refreshConflicts(d.jobs[0])
	select {
	case ev := <-events:
		if ev.Conflicts != 0 || ev.State != StateSynced {
			t.Errorf("event after resolving = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event after the count changed")
	}
}

func capture(d *Daemon) *[]history.Entry {
	var got []history.Entry
	var mu sync.Mutex
	d.Record = func(es ...history.Entry) {
		mu.Lock()
		got = append(got, es...)
		mu.Unlock()
	}
	return &got
}

func TestRecordOpsForAPushJob(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, 0)
	got := capture(d)
	d.recordOps(d.jobs[0], delta.Ops{
		Uploads:     []string{"a.txt", "b.txt"},
		Created:     []string{"a.txt"},
		Mkdirs:      []string{"dir"},
		Moves:       []delta.Move{{From: "x", To: "y"}},
		DeleteFiles: []string{"z.txt"},
		DeleteDirs:  []string{"old"},
	})
	want := []history.Entry{
		{Job: "a", Op: history.OpNew, Path: "a.txt", Way: history.Up},
		{Job: "a", Op: history.OpModified, Path: "b.txt", Way: history.Up},
		{Job: "a", Op: history.OpFolder, Path: "dir", Dir: true, Way: history.Up},
		{Job: "a", Op: history.OpMoved, Path: "x", To: "y", Way: history.Up},
		{Job: "a", Op: history.OpDeleted, Path: "z.txt", Way: history.Up},
		{Job: "a", Op: history.OpDeleted, Path: "old", Dir: true, Way: history.Up},
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("got %+v\nwant %+v", *got, want)
	}
}

func TestRecordOpsForBisyncLeavesDeletesAndMovesToTheFullPass(t *testing.T) {
	d := New(job(config.RoleBisync, ""), ok, 0)
	got := capture(d)
	d.recordOps(d.jobs[0], delta.Ops{
		Uploads: []string{"a.txt"}, Created: []string{"a.txt"},
		Moves: []delta.Move{{From: "x", To: "y"}}, DeleteFiles: []string{"z"},
	})
	if len(*got) != 1 || (*got)[0].Op != history.OpNew {
		t.Errorf("got %+v", *got)
	}
}

func TestRecordIncomingFromAnotherMachine(t *testing.T) {
	jobs := []config.Job{{Name: "docs", Role: config.RoleBisync, RemotePath: "docs"}}
	d := New(jobs, ok, 0)
	got := capture(d)
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, rec := range []journal.Record{
		{Machine: "laptop", Time: at.Unix(), Op: journal.OpUpload, Path: "docs/a.txt"},
		{Machine: "laptop", Op: journal.OpUpload, Path: "elsewhere/b.txt"}, // outside the job
		{Machine: "laptop", Op: journal.OpDelete, Path: "docs/sub/c.txt"},
		{Machine: "laptop", Op: journal.OpMove, Path: "docs/old.txt", To: "docs/new.txt"},
	} {
		d.recordIncoming(d.jobs[0], rec)
	}
	want := []history.Entry{
		{Time: at.Local(), Job: "docs", Op: history.OpUploaded, Path: "a.txt", Way: history.Remote, Machine: "laptop"},
		{Job: "docs", Op: history.OpDeleted, Path: "sub/c.txt", Way: history.Remote, Machine: "laptop"},
		{Job: "docs", Op: history.OpMoved, Path: "old.txt", To: "new.txt", Way: history.Remote, Machine: "laptop"},
	}
	if len(*got) != 3 || !(*got)[0].Time.Equal(at) {
		t.Fatalf("got %+v", *got)
	}
	for i := range want {
		g := (*got)[i]
		g.Time, want[i].Time = time.Time{}, time.Time{}
		if !reflect.DeepEqual(g, want[i]) {
			t.Errorf("entry %d: got %+v want %+v", i, g, want[i])
		}
	}
}
