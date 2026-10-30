package watch

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type harness struct {
	batches chan Batch
	mu      sync.Mutex
	active  int
	idle    int
}

func start(t *testing.T, root string, o Options, handle func(Batch)) *harness {
	t.Helper()
	h := &harness{batches: make(chan Batch, 20)}
	o.OnActive = func() { h.mu.Lock(); h.active++; h.mu.Unlock() }
	o.OnIdle = func() { h.mu.Lock(); h.idle++; h.mu.Unlock() }
	ready := make(chan struct{})
	o.OnReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Batches(ctx, root, o, func(b Batch) {
			if handle != nil {
				handle(b)
			}
			h.batches <- b
		}, t.Logf)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("the watcher never became ready")
	}
	return h
}

func (h *harness) next(t *testing.T) Batch {
	t.Helper()
	select {
	case b := <-h.batches:
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("no batch delivered")
		return Batch{}
	}
}

func (h *harness) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case b := <-h.batches:
		t.Fatalf("unexpected batch: %+v", b)
	case <-time.After(d):
	}
}

func contains(b Batch, p string) bool {
	for _, x := range b.Paths {
		if x == p {
			return true
		}
	}
	return false
}

func TestABurstIsOneBatchOfDistinctPaths(t *testing.T) {
	root := t.TempDir()
	h := start(t, root, Options{Quiet: 300 * time.Millisecond}, nil)
	f := filepath.Join(root, "f")
	for i := 0; i < 5; i++ {
		os.WriteFile(f, []byte{byte(i)}, 0o644)
		time.Sleep(40 * time.Millisecond)
	}
	os.WriteFile(filepath.Join(root, "g"), []byte("g"), 0o644)

	b := h.next(t)
	if !contains(b, f) || !contains(b, filepath.Join(root, "g")) {
		t.Errorf("paths = %v", b.Paths)
	}
	seen := map[string]bool{}
	for _, p := range b.Paths {
		if seen[p] {
			t.Errorf("%s listed twice", p)
		}
		seen[p] = true
	}
	h.none(t, 700*time.Millisecond)
}

func TestNewSubdirectoriesAreWatchedAndTheirFilesReported(t *testing.T) {
	root := t.TempDir()
	h := start(t, root, Options{Quiet: 150 * time.Millisecond}, nil)

	sub := filepath.Join(root, "a", "b")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "early"), []byte("x"), 0o644)
	b := h.next(t)
	if !contains(b, filepath.Join(sub, "early")) {
		t.Errorf("a file created inside a brand-new folder was missed: %v", b.Paths)
	}

	time.Sleep(300 * time.Millisecond)
	os.WriteFile(filepath.Join(sub, "later"), []byte("y"), 0o644)
	if b := h.next(t); !contains(b, filepath.Join(sub, "later")) {
		t.Errorf("the new folder is not being watched: %v", b.Paths)
	}
}

func TestMaxWaitDeliversDuringConstantActivity(t *testing.T) {
	root := t.TempDir()
	h := start(t, root, Options{Quiet: time.Second, MaxWait: 400 * time.Millisecond}, nil)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		f := filepath.Join(root, "busy")
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				os.WriteFile(f, []byte{byte(i)}, 0o644)
			}
		}
	}()
	h.next(t) // the quiet period never arrives; MaxWait must deliver
}

func TestEventsDuringAHandlerFormTheNextBatch(t *testing.T) {
	root := t.TempDir()
	release := make(chan struct{})
	var once sync.Once
	h := start(t, root, Options{Quiet: 100 * time.Millisecond}, func(Batch) {
		once.Do(func() { <-release }) // the first batch is slow to handle
	})
	os.WriteFile(filepath.Join(root, "one"), []byte("1"), 0o644)
	time.Sleep(400 * time.Millisecond) // the first batch is now being handled
	os.WriteFile(filepath.Join(root, "two"), []byte("2"), 0o644)
	time.Sleep(300 * time.Millisecond)
	close(release)

	first := h.next(t)
	second := h.next(t)
	if !contains(first, filepath.Join(root, "one")) || !contains(second, filepath.Join(root, "two")) {
		t.Errorf("batches = %v then %v", first.Paths, second.Paths)
	}
}

func TestActiveAndIdleBracketTheWork(t *testing.T) {
	root := t.TempDir()
	h := start(t, root, Options{Quiet: 100 * time.Millisecond}, nil)
	os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644)
	h.next(t)
	time.Sleep(200 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active != 1 || h.idle != 1 {
		t.Errorf("active = %d, idle = %d, want 1 and 1", h.active, h.idle)
	}
}

func TestMissingRootIsAnError(t *testing.T) {
	err := Batches(context.Background(), filepath.Join(t.TempDir(), "nope"), Options{Quiet: time.Second},
		func(Batch) {}, t.Logf)
	if err == nil {
		t.Error("expected an error")
	}
}
