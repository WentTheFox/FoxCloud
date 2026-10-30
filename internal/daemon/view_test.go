package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/config"
)

func waitView(t *testing.T, path, known string) <-chan View {
	t.Helper()
	got := make(chan View, 1)
	go func() {
		v, err := WaitView(context.Background(), path, known)
		if err != nil {
			t.Errorf("WaitView: %v", err)
		}
		got <- v
	}()
	return got
}

func expectView(t *testing.T, got <-chan View) View {
	t.Helper()
	select {
	case v := <-got:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("WaitView did not return")
		return View{}
	}
}

func expectBlocked(t *testing.T, got <-chan View) {
	t.Helper()
	select {
	case v := <-got:
		t.Fatalf("WaitView returned early: %+v", v)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestBuildViewSeparatesUpdateAndChangesRev(t *testing.T) {
	a := BuildView([]Event{{Job: "x", State: StateIdle}})
	b := BuildView([]Event{{Job: "x", State: StateSynced}})
	c := BuildView([]Event{{Job: "x", State: StateIdle}, {Update: "v1.0.0"}})
	if a.Rev == b.Rev || a.Rev == c.Rev {
		t.Error("a state or update change must change the rev")
	}
	if c.Update != "v1.0.0" || len(c.Jobs) != 1 {
		t.Errorf("view = %+v", c)
	}
	if !a.Running || OfflineView().Running {
		t.Error("Running flags are wrong")
	}
}

func TestCurrentViewWithoutDaemonIsOffline(t *testing.T) {
	v, err := CurrentView(sockPath(t))
	if err != nil || v.Running || v.Rev != OfflineRev {
		t.Errorf("view = %+v, err = %v", v, err)
	}
}

func TestWaitViewReturnsAtOnceWhenKnownIsStale(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, time.Hour)
	path := serve(t, d)
	v := expectView(t, waitView(t, path, "stale"))
	if !v.Running {
		t.Errorf("view = %+v", v)
	}
}

func TestWaitViewBlocksUntilTheNextChange(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, time.Hour)
	path := serve(t, d)
	cur, _ := CurrentView(path)

	got := waitView(t, path, cur.Rev)
	expectBlocked(t, got)

	d.SetUpdate("v9.9.9")
	v := expectView(t, got)
	if v.Update != "v9.9.9" || v.Rev == cur.Rev {
		t.Errorf("view = %+v", v)
	}
}

func TestWaitViewWaitsForADaemonToAppear(t *testing.T) {
	path := sockPath(t)
	got := waitView(t, path, OfflineRev)
	expectBlocked(t, got)

	d := New(job(config.RolePush, ""), ok, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	go Serve(ctx, d, path)
	if v := expectView(t, got); !v.Running {
		t.Errorf("view = %+v, want a running daemon", v)
	}
}

func TestWaitViewReportsTheDaemonGoing(t *testing.T) {
	d := New(job(config.RolePush, ""), ok, time.Hour)
	path := sockPath(t)
	ctx, stop := context.WithCancel(context.Background())
	go Serve(ctx, d, path)
	for i := 0; i < 200; i++ {
		if _, err := Do(path, Request{Cmd: "status"}); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cur, _ := CurrentView(path)
	got := waitView(t, path, cur.Rev)
	expectBlocked(t, got)
	stop()
	if v := expectView(t, got); v.Running {
		t.Errorf("view = %+v, want offline", v)
	}
}
