// Package watch turns changes under a folder into one debounced callback, so
// a push job can sync soon after files change without polling the disk.
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

// Run watches root and everything below it, calling onChange once the changes
// have been quiet for the debounce period. It returns when ctx is cancelled,
// or with an error if the watcher cannot be set up at all. Directories that
// cannot be watched (the OS limits how many there may be) are reported to
// logf and skipped: the job's interval still covers them.
func Run(ctx context.Context, root string, debounce time.Duration, onChange func(), logf func(format string, args ...any)) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	if err := addTree(w, root, logf); err != nil {
		return err
	}

	var timer *time.Timer
	var fire <-chan time.Time
	arm := func() {
		if timer == nil {
			timer = time.NewTimer(debounce)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(debounce)
		}
		fire = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Has(fsnotify.Create) {
				// A new folder needs its own watch, and anything created in it
				// before the watch existed would otherwise be missed.
				if fi, err := os.Lstat(ev.Name); err == nil && fi.IsDir() {
					addTree(w, ev.Name, logf)
				}
			}
			arm()
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			// Events were lost (an overflow, say): a pass is the safe answer.
			logf("watch %s: %v", root, err)
			arm()
		case <-fire:
			fire = nil
			onChange()
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
