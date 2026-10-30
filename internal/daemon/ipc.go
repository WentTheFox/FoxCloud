package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ErrNotRunning means nothing is listening on the daemon's socket.
var ErrNotRunning = errors.New("the foxcloud daemon is not running")

// Serve answers clients on a unix socket (Windows 10 and later support them
// too) until ctx is cancelled. It refuses to start if another daemon already
// owns the socket, and clears a stale socket file left by a crash.
func Serve(ctx context.Context, d *Daemon, path string) error {
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return fmt.Errorf("another foxcloud daemon is already running (%s)", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		if len(path) > 100 {
			return fmt.Errorf("cannot listen on %s: unix socket paths are limited to about 108 characters and this one is %d: %w", path, len(path), err)
		}
		return err
	}
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go handle(ctx, d, conn)
	}
}

func handle(ctx context.Context, d *Daemon, conn net.Conn) {
	defer conn.Close()
	// A client that connects and never speaks must not hold a goroutine forever.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	conn.SetReadDeadline(time.Time{})
	enc := json.NewEncoder(conn)

	switch req.Cmd {
	case "status":
		enc.Encode(Response{OK: true, Events: d.Snapshot()})
	case "sync-now":
		reply(enc, d.SyncNow(req.Job))
	case "stop":
		reply(enc, d.Stop(req.Job))
	case "subscribe":
		snap, events, cancel := d.Subscribe()
		defer cancel()
		if enc.Encode(Response{OK: true, Events: snap}) != nil {
			return
		}
		// The client sends nothing more, so a read only returns when it hangs up.
		gone := make(chan struct{})
		go func() {
			conn.Read(make([]byte, 1))
			close(gone)
		}()
		for {
			select {
			case ev := <-events:
				if enc.Encode(ev) != nil {
					return
				}
			case <-gone:
				return
			case <-ctx.Done():
				return
			}
		}
	default:
		enc.Encode(Response{Error: fmt.Sprintf("unknown command %q", req.Cmd)})
	}
}

func reply(enc *json.Encoder, err error) {
	if err != nil {
		enc.Encode(Response{Error: err.Error()})
		return
	}
	enc.Encode(Response{OK: true})
}

func dial(path string) (net.Conn, error) {
	c, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		return nil, ErrNotRunning
	}
	return c, nil
}

// Do sends one request and returns the daemon's answer.
func Do(path string, req Request) (Response, error) {
	var resp Response
	c, err := dial(path)
	if err != nil {
		return resp, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return resp, err
	}
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return resp, err
	}
	if !resp.OK {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// Watch subscribes to the daemon and calls fn for the current state of every
// job and then for each change, until ctx is cancelled or the daemon goes
// away. Losing the connection returns ErrNotRunning: that closed connection is
// how a client learns the daemon is gone, with no polling.
func Watch(ctx context.Context, path string, fn func(Event)) error {
	return WatchSnapshot(ctx, path, func(evs []Event) {
		for _, ev := range evs {
			fn(ev)
		}
	}, fn)
}

// WatchSnapshot is Watch with the starting state handed over whole, so a
// client can tell it apart from later changes and never act on a half-built
// picture.
func WatchSnapshot(ctx context.Context, path string, snapshot func([]Event), change func(Event)) error {
	c, err := dial(path)
	if err != nil {
		return err
	}
	defer c.Close()
	go func() {
		<-ctx.Done()
		c.Close()
	}()
	if err := json.NewEncoder(c).Encode(Request{Cmd: "subscribe"}); err != nil {
		return err
	}
	dec := json.NewDecoder(c)
	var first Response
	if err := dec.Decode(&first); err != nil {
		return ErrNotRunning
	}
	snapshot(first.Events)
	for {
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return ErrNotRunning
		}
		change(ev)
	}
}
