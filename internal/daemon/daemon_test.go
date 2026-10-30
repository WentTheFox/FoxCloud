package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/classify"
	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/engine"
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

func TestActiveStateFollowsTheRole(t *testing.T) {
	for role, want := range map[string]State{
		config.RolePush: StateUploading, config.RolePull: StateDownloading, config.RoleBisync: StateSyncing,
	} {
		release := make(chan struct{})
		d := New(job(role, ""), func(ctx context.Context, _ string, _ func(), _ func(rclone.Direction)) error {
			<-release
			return nil
		}, 0)
		await := start(t, d)
		await(want)
		close(release)
		await(StateSynced)
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

func TestPushIgnoresReportedDirection(t *testing.T) {
	d := New(job(config.RolePush, ""), func(_ context.Context, _ string, _ func(), onDir func(rclone.Direction)) error {
		onDir(rclone.Download)
		return nil
	}, 0)
	await := start(t, d)
	await(StateUploading)
	await(StateSynced) // a download event in between would have been skipped past
}

func TestWatchedPushJobSyncsAfterChange(t *testing.T) {
	dir := t.TempDir()
	var passes atomic.Int32
	jobs := []config.Job{{Name: "a", Role: config.RolePush, LocalPath: dir, Watch: true}}
	d := New(jobs, func(context.Context, string, func(), func(rclone.Direction)) error {
		passes.Add(1)
		return nil
	}, 0)
	d.WatchDebounce = 100 * time.Millisecond
	await := start(t, d)
	await(StateSynced)
	time.Sleep(300 * time.Millisecond) // the watch starts after the first pass
	os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	await(StateSynced)
	if passes.Load() < 2 {
		t.Errorf("passes = %d, want a second one after the file changed", passes.Load())
	}
}

func TestWatchIsIgnoredForOtherRoles(t *testing.T) {
	dir := t.TempDir()
	var passes atomic.Int32
	jobs := []config.Job{{Name: "a", Role: config.RoleBisync, LocalPath: dir, Watch: true}}
	d := New(jobs, func(context.Context, string, func(), func(rclone.Direction)) error {
		passes.Add(1)
		return nil
	}, 0)
	d.WatchDebounce = 50 * time.Millisecond
	await := start(t, d)
	await(StateSynced)
	os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	time.Sleep(500 * time.Millisecond)
	if passes.Load() != 1 {
		t.Errorf("passes = %d, want 1", passes.Load())
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
