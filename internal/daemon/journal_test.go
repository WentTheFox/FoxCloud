package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/delta"
	"github.com/WentTheFox/FoxCloud/internal/journal"
	"github.com/WentTheFox/FoxCloud/internal/rclone"
)

type fetchCall struct {
	paths     []string
	overwrite bool
}

type fetchLog struct {
	mu    sync.Mutex
	calls []fetchCall
}

func (f *fetchLog) fetch(_ context.Context, _ string, paths []string, overwrite bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, fetchCall{append([]string(nil), paths...), overwrite})
	f.mu.Unlock()
	return nil
}

func (f *fetchLog) get() []fetchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fetchCall(nil), f.calls...)
}

// mirror starts a job of the given role over a temp folder, with a runner that
// counts full passes, and waits for the start-up pass to be done.
func mirror(t *testing.T, role, remotePath string) (d *Daemon, dir string, passes *atomic.Int32, fl *fetchLog) {
	t.Helper()
	IncomingDelay = 30 * time.Millisecond
	t.Cleanup(func() { IncomingDelay = 2 * time.Second })
	dir = t.TempDir()
	passes = new(atomic.Int32)
	fl = &fetchLog{}
	jobs := []config.Job{{Name: "a", Role: role, LocalPath: dir, RemotePath: remotePath, BackupDir: t.TempDir()}}
	d = New(jobs, func(context.Context, string, func(), func(rclone.Direction)) error {
		passes.Add(1)
		return nil
	}, 0)
	d.Fetch = fl.fetch
	await := start(t, d)
	await(StateSynced)
	return d, dir, passes, fl
}

func settle() { time.Sleep(250 * time.Millisecond) }

func TestBisyncFetchesNewFilesAndLeavesTheRestToAFullPass(t *testing.T) {
	d, dir, passes, fl := mirror(t, config.RoleBisync, "")
	os.WriteFile(filepath.Join(dir, "have.txt"), []byte("x"), 0o644)

	d.Incoming(journal.Record{Op: journal.OpUpload, Path: "new/a.txt"})
	settle()
	calls := fl.get()
	if len(calls) != 1 || len(calls[0].paths) != 1 || calls[0].paths[0] != "new/a.txt" || calls[0].overwrite {
		t.Fatalf("fetch calls = %+v, want one non-overwriting fetch of new/a.txt", calls)
	}
	if passes.Load() != 1 {
		t.Errorf("a plain new file must not cost a full pass, passes = %d", passes.Load())
	}

	// A file that also exists here may be a conflict: bisync decides.
	d.Incoming(journal.Record{Op: journal.OpUpload, Path: "have.txt"})
	settle()
	if len(fl.get()) != 1 || passes.Load() != 2 {
		t.Errorf("a changed existing file: fetches = %d, passes = %d, want 1 and 2", len(fl.get()), passes.Load())
	}
}

func TestBisyncDeletesAndMovesBringAFullPassForward(t *testing.T) {
	for _, rec := range []journal.Record{
		{Op: journal.OpDelete, Path: "gone.txt"},
		{Op: journal.OpRmdir, Path: "gone", Dir: true},
		{Op: journal.OpMove, Path: "a", To: "b", Dir: true},
	} {
		d, _, passes, fl := mirror(t, config.RoleBisync, "")
		d.Incoming(rec)
		settle()
		if passes.Load() != 2 || len(fl.get()) != 0 {
			t.Errorf("%s: passes = %d, fetches = %d, want 2 and 0", rec.Op, passes.Load(), len(fl.get()))
		}
	}
}

func TestPullFetchesWithOverwriteAndIgnoresDeletes(t *testing.T) {
	d, _, passes, fl := mirror(t, config.RolePull, "")
	d.Incoming(journal.Record{Op: journal.OpUpload, Path: "x.txt"})
	d.Incoming(journal.Record{Op: journal.OpDelete, Path: "never-touched.txt"})
	settle()
	calls := fl.get()
	if len(calls) != 1 || !calls[0].overwrite || calls[0].paths[0] != "x.txt" {
		t.Errorf("fetch calls = %+v", calls)
	}
	if passes.Load() != 1 {
		t.Errorf("a pull job never deletes, so a delete record is no work; passes = %d", passes.Load())
	}
}

func TestRecordsOutsideTheJobsFolderAreIgnoredAndPathsAreRebased(t *testing.T) {
	d, _, passes, fl := mirror(t, config.RoleBisync, "/photos/")
	d.Incoming(journal.Record{Op: journal.OpUpload, Path: "documents/x.txt"})
	d.Incoming(journal.Record{Op: journal.OpDelete, Path: "photos"})
	d.Incoming(journal.Record{Op: journal.OpUpload, Path: "photos/2026/a.jpg"})
	settle()
	calls := fl.get()
	if len(calls) != 1 || calls[0].paths[0] != "2026/a.jpg" {
		t.Errorf("fetch calls = %+v, want only 2026/a.jpg, relative to the job's folder", calls)
	}
	if passes.Load() != 1 {
		t.Errorf("passes = %d, want 1", passes.Load())
	}
}

func TestMkdirRecordsCreateTheFolder(t *testing.T) {
	d, dir, _, _ := mirror(t, config.RoleBisync, "")
	d.Incoming(journal.Record{Op: journal.OpMkdir, Path: "empty/inner", Dir: true})
	settle()
	if fi, err := os.Stat(filepath.Join(dir, "empty", "inner")); err != nil || !fi.IsDir() {
		t.Errorf("the folder was not created: %v", err)
	}
}

func TestUnknownRecordKindsAreIgnored(t *testing.T) {
	d, _, passes, fl := mirror(t, config.RoleBisync, "")
	d.Incoming(journal.Record{Op: "something-from-a-newer-version", Path: "x"})
	settle()
	if passes.Load() != 1 || len(fl.get()) != 0 {
		t.Errorf("passes = %d, fetches = %d", passes.Load(), len(fl.get()))
	}
}

func TestAGapBringsFullPassesForward(t *testing.T) {
	d, _, passes, _ := mirror(t, config.RoleBisync, "")
	d.JournalGap("records from desktop are missing")
	settle()
	if passes.Load() != 2 {
		t.Errorf("passes = %d, want 2", passes.Load())
	}
}

func TestJournalNeverActsBeforeTheStartupPassIsDone(t *testing.T) {
	IncomingDelay = 10 * time.Millisecond
	t.Cleanup(func() { IncomingDelay = 2 * time.Second })
	fl := &fetchLog{}
	var passes atomic.Int32
	jobs := []config.Job{{Name: "a", Role: config.RoleBisync, LocalPath: t.TempDir()}}
	d := New(jobs, func(context.Context, string, func(), func(rclone.Direction)) error {
		passes.Add(1)
		return nil
	}, 400*time.Millisecond) // the start-up hold
	d.Fetch = fl.fetch
	await := start(t, d)

	d.Incoming(journal.Record{Op: journal.OpUpload, Path: "early.txt"})
	d.JournalGap("test")
	time.Sleep(200 * time.Millisecond)
	if len(fl.get()) != 0 || passes.Load() != 0 {
		t.Fatalf("acted during the hold: fetches = %d, passes = %d", len(fl.get()), passes.Load())
	}
	await(StateSynced)
	settle()
	if len(fl.get()) != 1 {
		t.Errorf("the early record should be acted on once the hold is over; fetches = %d", len(fl.get()))
	}
}

func TestAppliedChangesAreWrittenToTheJournalInOrder(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	got := make(chan struct{}, 8)
	dir := t.TempDir()
	jobs := []config.Job{{Name: "a", Role: config.RolePush, LocalPath: dir, Watch: true}}
	d := New(jobs, ok, 0)
	d.WatchQuiet = 100 * time.Millisecond
	d.Apply = func(context.Context, string, delta.Ops) error { return nil }
	d.JournalWrite = func(_ context.Context, j config.Job, ops delta.Ops) error {
		mu.Lock()
		seen = append(seen, ops.Uploads...)
		mu.Unlock()
		got <- struct{}{}
		return nil
	}
	await := start(t, d)
	await(StateSynced)
	time.Sleep(300 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "one.txt"), []byte("1"), 0o644)
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the journal was not written")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "one.txt" {
		t.Errorf("journal saw %v", seen)
	}
}
