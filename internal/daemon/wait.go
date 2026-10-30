package daemon

import (
	"context"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// Follow keeps a client connected: it watches the daemon and calls fn for its
// events, and when the daemon is not running (or goes away) it calls gone and
// then waits, with no polling, for the socket file to reappear before
// connecting again. It returns only when ctx is cancelled or the socket's
// directory cannot be watched.
func Follow(ctx context.Context, path string, fn func(Event), gone func()) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		return err
	}
	for {
		// Watching first means a socket created while we connect is not missed.
		err := Watch(ctx, path, fn)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			gone()
		}
		if err := waitForSocket(ctx, w, path); err != nil {
			return err
		}
	}
}

// waitForSocket blocks until a daemon can be connected to: the socket file is
// created, or a connection already works.
func waitForSocket(ctx context.Context, w *fsnotify.Watcher, path string) error {
	// Drain events from before now: only a creation after this point counts.
	for drained := false; !drained; {
		select {
		case <-w.Events:
		default:
			drained = true
		}
	}
	// A daemon that came up between the failed connection and the drain
	// above left no event behind, so look once more.
	if c, err := dial(path); err == nil {
		c.Close()
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if filepath.Clean(ev.Name) == filepath.Clean(path) && ev.Has(fsnotify.Create) {
				return nil
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			return err
		}
	}
}
