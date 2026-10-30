package daemon

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/classify"
	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/engine"
)

func job(role, interval string) []config.Job {
	return []config.Job{{Name: "a", Role: role, Interval: interval}}
}

// start runs the daemon in the background and returns a way to wait for states.
func start(t *testing.T, d *Daemon) (await func(State) Event) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	_, events, unsub := d.Subscribe()
	t.Cleanup(unsub)
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

func ok(context.Context, string, func()) error { return nil }

func TestHoldsBeforeTheFirstPass(t *testing.T) {
	var ranAt atomic.Int64
	began := time.Now()
	d := New(job(config.RolePush, ""), func(context.Context, string, func()) error {
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
	d := New(job(config.RolePush, "30ms"), func(context.Context, string, func()) error {
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
		d := New(job(role, ""), func(ctx context.Context, _ string, _ func()) error {
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
			d := New(job(config.RolePush, ""), func(context.Context, string, func()) error { return tt.err }, 0)
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
	d := New(job(config.RolePush, ""), func(ctx context.Context, _ string, _ func()) error {
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
	d := New(job(config.RoleBisync, ""), func(_ context.Context, _ string, onResync func()) error {
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
