package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func start(t *testing.T, root string, debounce time.Duration) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan struct{}, 10)
	done := make(chan struct{})
	go func() {
		Run(ctx, root, debounce, func() { got <- struct{}{} }, t.Logf)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(200 * time.Millisecond) // let the watches register
	return got
}

func expect(t *testing.T, got <-chan struct{}) {
	t.Helper()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no change reported")
	}
}

func TestReportsChangesOnceDebounced(t *testing.T) {
	root := t.TempDir()
	got := start(t, root, 300*time.Millisecond)
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(root, "f"), []byte{byte(i)}, 0o644)
		time.Sleep(50 * time.Millisecond)
	}
	expect(t, got)
	select {
	case <-got:
		t.Error("a burst of writes must produce a single callback")
	case <-time.After(700 * time.Millisecond):
	}
}

func TestWatchesNewSubdirectories(t *testing.T) {
	root := t.TempDir()
	got := start(t, root, 100*time.Millisecond)
	sub := filepath.Join(root, "a", "b")
	os.MkdirAll(sub, 0o755)
	expect(t, got)
	time.Sleep(300 * time.Millisecond)
	for len(got) > 0 {
		<-got
	}
	os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o644)
	expect(t, got)
}

func TestMissingRootIsAnError(t *testing.T) {
	err := Run(context.Background(), filepath.Join(t.TempDir(), "nope"), time.Second, func() {}, t.Logf)
	if err == nil {
		t.Error("expected an error")
	}
}
