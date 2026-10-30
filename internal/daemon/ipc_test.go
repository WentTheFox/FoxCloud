package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/config"
)

// sockPath returns a short socket path: unix socket paths are limited to about
// 108 characters, and t.TempDir() embeds the whole test name.
func sockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}
func serve(t *testing.T, d *Daemon) string {
	t.Helper()
	path := sockPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, d, path) }()
	t.Cleanup(func() { cancel(); <-errc })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := Do(path, Request{Cmd: "status"}); err == nil {
			return path
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not come up")
	return ""
}

func TestStatusAndSyncNowOverTheSocket(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	path := serve(t, d)

	// Run emits the waiting event itself, so give it a moment.
	var resp Response
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, _ = Do(path, Request{Cmd: "status"})
		if len(resp.Events) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(resp.Events) != 1 || resp.Events[0].State != StateWaiting {
		t.Fatalf("status = %+v, want one waiting job", resp.Events)
	}

	if _, err := Do(path, Request{Cmd: "sync-now", Job: "a"}); err != nil {
		t.Fatal(err)
	}
	watchCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	synced := make(chan struct{})
	go Watch(watchCtx, path, func(ev Event) {
		if ev.State == StateSynced {
			select {
			case <-synced:
			default:
				close(synced)
			}
		}
	})
	select {
	case <-synced:
	case <-watchCtx.Done():
		t.Fatal("never saw the synced state over the stream")
	}
}

func TestUnknownJobAndCommandAreErrors(t *testing.T) {
	path := serve(t, New(job(config.RolePush, ""), ok, time.Hour))
	if _, err := Do(path, Request{Cmd: "sync-now", Job: "nope"}); err == nil {
		t.Error("expected an error for an unknown job")
	}
	if _, err := Do(path, Request{Cmd: "bogus"}); err == nil {
		t.Error("expected an error for an unknown command")
	}
}

func TestSecondDaemonIsRefused(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, time.Hour)
	path := serve(t, d)
	if err := Serve(context.Background(), d, path); err == nil {
		t.Error("a second daemon on the same socket must be refused")
	}
}

func TestClientsLearnTheDaemonIsGoneWhenTheConnectionCloses(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, time.Hour)
	path := sockPath(t)
	srvCtx, stopSrv := context.WithCancel(context.Background())
	srvDone := make(chan error, 1)
	go func() { srvDone <- Serve(srvCtx, d, path) }()
	for i := 0; i < 300; i++ {
		if _, err := Do(path, Request{Cmd: "status"}); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got := make(chan error, 1)
	connected := make(chan struct{}, 1)
	go func() {
		got <- Watch(context.Background(), path, func(Event) {
			select {
			case connected <- struct{}{}:
			default:
			}
		})
	}()
	d.emit(Event{Job: "a", State: StateIdle}) // gives the watcher something to receive
	select {
	case <-connected:
	case err := <-got:
		t.Fatalf("Watch returned early: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("watcher never connected")
	}
	stopSrv()
	<-srvDone
	select {
	case err := <-got:
		if err != ErrNotRunning {
			t.Errorf("Watch returned %v, want ErrNotRunning", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the client was not told when the daemon went away")
	}
}

func TestNothingListeningIsNotRunning(t *testing.T) {
	_, err := Do(sockPath(t), Request{Cmd: "status"})
	if err != ErrNotRunning {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
}

func TestFollowReconnectsWhenDaemonAppears(t *testing.T) {
	path := sockPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var log []string
	note := func(s string) { mu.Lock(); log = append(log, s); mu.Unlock() }
	done := make(chan struct{})
	go func() {
		Follow(ctx, path, func(ev Event) { note("event:" + string(ev.State)) }, func() { note("gone") })
		close(done)
	}()

	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			for _, l := range log {
				if l == want {
					mu.Unlock()
					return
				}
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("never saw %q; log = %v", want, log)
	}

	waitFor("gone") // nothing is listening yet

	d := New(job(config.RolePush, ""), ok, time.Hour)
	srvCtx, stopSrv := context.WithCancel(context.Background())
	go d.Run(srvCtx)
	go Serve(srvCtx, d, path)
	waitFor("event:waiting") // connected on its own once the socket appeared

	stopSrv()
	cancel()
	<-done
}
