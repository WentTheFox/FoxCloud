// Package watch reports which paths changed under a folder, in quiet-period
// batches, so a push job can upload just those files soon after they change
// without polling the disk.
package watch

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Batch is the paths that changed during one burst of activity.
type Batch struct {
	// Paths are absolute. A path may be reported more than once in a burst but
	// is listed once here; whether it was created, changed or removed is for
	// the caller to find out by looking at it.
	Paths []string
	// Overflow means the OS dropped events (its queue filled up), so Paths is
	// incomplete and a full scan is the only safe answer.
	Overflow bool
}

// Options tune Batches.
type Options struct {
	// Quiet is how long the folder must stay silent before a batch is delivered.
	Quiet time.Duration
	// MaxWait delivers a batch anyway this long after its first event, so
	// constant activity cannot hold uploads back forever. Zero means no limit.
	MaxWait time.Duration
	// OnReady is called once every folder is being watched, before any batch.
	// Anything that has to look at the tree (a scan) should do it here, so no
	// change can fall between the scan and the watch.
	OnReady func()
	// OnActive is called at the first event after a quiet spell, and OnIdle
	// once everything seen has been delivered and handled. Between them there
	// is work in flight, which is what the shutdown guard wants to know.
	OnActive func()
	OnIdle   func()
}

// Batches watches root and everything below it and calls onBatch with each
// burst of changes, one at a time, until ctx is cancelled. Events that arrive
// while onBatch is running are gathered into the next batch. It returns an
// error only if the watcher cannot be set up at all. Directories that cannot
// be watched (the OS limits how many there may be) are reported to logf and
// skipped: the job's full pass still covers them.
func Batches(ctx context.Context, root string, o Options, onBatch func(Batch), logf func(format string, args ...any)) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	if err := addTree(w, root, logf); err != nil {
		return err
	}
	if o.OnReady != nil {
		o.OnReady()
	}

	var (
		seen     = map[string]struct{}{}
		overflow bool
		first    time.Time
		busy     bool
		quiet    = time.NewTimer(time.Hour)
		maxWait  = time.NewTimer(time.Hour)
		done     = make(chan struct{})
	)
	quiet.Stop()
	maxWait.Stop()
	defer quiet.Stop()
	defer maxWait.Stop()

	pending := func() bool { return len(seen) > 0 || overflow }

	dispatch := func() {
		quiet.Stop()
		maxWait.Stop()
		b := Batch{Overflow: overflow}
		for p := range seen {
			b.Paths = append(b.Paths, p)
		}
		seen, overflow = map[string]struct{}{}, false
		busy = true
		go func() {
			onBatch(b)
			done <- struct{}{}
		}()
	}

	note := func() {
		if o.OnActive != nil && !busy && len(seen) <= 1 && first.IsZero() {
			o.OnActive()
		}
		if first.IsZero() {
			first = time.Now()
			if o.MaxWait > 0 {
				maxWait.Reset(o.MaxWait)
			}
		}
		if !quiet.Stop() {
			select {
			case <-quiet.C:
			default:
			}
		}
		quiet.Reset(o.Quiet)
	}

	for {
		select {
		case <-ctx.Done():
			if busy {
				<-done
			}
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Has(fsnotify.Create) {
				// A new folder needs its own watch, and anything created in it
				// before the watch existed is reported by walking it.
				if fi, err := os.Lstat(ev.Name); err == nil && fi.IsDir() {
					addTree(w, ev.Name, logf)
					filepath.WalkDir(ev.Name, func(p string, _ fs.DirEntry, err error) error {
						if err == nil {
							seen[p] = struct{}{}
						}
						return nil
					})
				}
			}
			seen[filepath.Clean(ev.Name)] = struct{}{}
			note()
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			// Events were lost (an overflow, say): only a full scan is safe.
			logf("watch %s: %v", root, err)
			overflow = true
			note()
		case <-quiet.C:
			if !busy && pending() {
				first = time.Time{}
				dispatch()
			}
		case <-maxWait.C:
			if !busy && pending() {
				first = time.Time{}
				dispatch()
			}
		case <-done:
			busy = false
			switch {
			case pending():
				// More arrived while that batch ran; it goes out once quiet.
				if o.MaxWait > 0 {
					maxWait.Reset(o.MaxWait)
				}
				quiet.Reset(o.Quiet)
			default:
				first = time.Time{}
				if o.OnIdle != nil {
					o.OnIdle()
				}
			}
		}
	}
}

func addTree(w *fsnotify.Watcher, dir string, logf func(string, ...any)) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			logf("watch: skipping %s: %v", path, err)
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if err := w.Add(path); err != nil {
			if errors.Is(err, fsnotify.ErrClosed) {
				return err
			}
			logf("watch: cannot watch %s: %v", path, err)
		}
		return nil
	})
}
