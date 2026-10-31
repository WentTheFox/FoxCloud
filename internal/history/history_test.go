package history

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	return &Store{Path: filepath.Join(t.TempDir(), "history.jsonl")}
}

func TestAppendAndLastNewestFirst(t *testing.T) {
	s := newStore(t)
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for i, p := range []string{"a", "b", "c"} {
		if err := s.Append(Entry{Time: base.Add(time.Duration(i) * time.Minute), Job: "j", Op: OpNew, Path: p}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Last(2, "")
	if err != nil || len(got) != 2 || got[0].Path != "c" || got[1].Path != "b" {
		t.Fatalf("Last(2) = %+v, %v", got, err)
	}
}

func TestLastFiltersByJobAndStampsTime(t *testing.T) {
	s := newStore(t)
	s.Append(Entry{Job: "x", Op: OpNew, Path: "1"}, Entry{Job: "y", Op: OpDeleted, Path: "2"})
	got, _ := s.Last(10, "y")
	if len(got) != 1 || got[0].Path != "2" || got[0].Time.IsZero() {
		t.Errorf("got %+v", got)
	}
}

func TestEmptyAndMissingFile(t *testing.T) {
	s := newStore(t)
	if got, err := s.Last(5, ""); err != nil || len(got) != 0 {
		t.Errorf("missing file: %v %v", got, err)
	}
	if s.Rev() != OfflineRev {
		t.Error("a missing file has the offline rev")
	}
}

func TestTornLinesAreSkipped(t *testing.T) {
	s := newStore(t)
	s.Append(Entry{Job: "j", Op: OpNew, Path: "ok"})
	f, _ := os.OpenFile(s.Path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{\"t\":\"2026-\n\n{not json}\n")
	f.Close()
	s.Append(Entry{Job: "j", Op: OpModified, Path: "after"})
	got, _ := s.Last(10, "")
	if len(got) != 2 || got[0].Path != "after" || got[1].Path != "ok" {
		t.Errorf("got %+v", got)
	}
}

func TestTrimKeepsTheNewest(t *testing.T) {
	s := newStore(t)
	var batch []Entry
	for i := 0; i < MaxEntries+10; i++ {
		batch = append(batch, Entry{Job: "j", Op: OpNew, Path: string(rune('a' + i%26)), To: string(rune(i))})
	}
	if err := s.Append(batch...); err != nil {
		t.Fatal(err)
	}
	all, _ := s.readAll()
	if len(all) != KeepEntries {
		t.Fatalf("after trim: %d entries, want %d", len(all), KeepEntries)
	}
	if all[len(all)-1].To != string(rune(MaxEntries+9)) {
		t.Error("the newest entry must survive")
	}
	// and appending still works afterwards
	s.Append(Entry{Job: "j", Op: OpNew, Path: "later"})
	if got, _ := s.Last(1, ""); got[0].Path != "later" {
		t.Error("append after trim")
	}
}

func TestWaitWakesOnAppend(t *testing.T) {
	s := newStore(t)
	rev := s.Rev()
	done := make(chan string, 1)
	go func() {
		r, err := s.Wait(context.Background(), rev)
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	time.Sleep(100 * time.Millisecond) // let the waiter settle in
	s.Append(Entry{Job: "j", Op: OpNew, Path: "x"})
	select {
	case r := <-done:
		if r == rev {
			t.Error("rev should have changed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not wake up")
	}
}

func TestWaitReturnsAtOnceWhenAlreadyStale(t *testing.T) {
	s := newStore(t)
	s.Append(Entry{Job: "j", Op: OpNew, Path: "x"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if r, err := s.Wait(ctx, OfflineRev); err != nil || r == OfflineRev {
		t.Errorf("got %q, %v", r, err)
	}
}

func TestWaitStopsWithTheContext(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := s.Wait(ctx, s.Rev()); err == nil {
		t.Error("expected the context error")
	}
}
