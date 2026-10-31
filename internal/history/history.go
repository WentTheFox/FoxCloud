// Package history keeps a short, local feed of what has happened to a job's
// files: uploaded, downloaded, changed, deleted, moved, trashed. Entries come
// from file-by-file uploads, from the per-file lines of full passes, from other
// machines' journal records and from conflict resolution. It is for people to
// read, not for the sync itself, so a lost or trimmed entry costs nothing.
package history

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// What happened to a file.
const (
	OpNew      = "new"      // a file that did not exist before
	OpModified = "modified" // an existing file with new content
	OpUploaded = "uploaded" // another machine sent a file; whether it was new is not known
	OpDeleted  = "deleted"
	OpMoved    = "moved" // renamed or moved: Path -> To
	OpFolder   = "folder"
	OpTrashed  = "trashed"  // moved to the system trash (conflict resolution)
	OpArchived = "archived" // set aside in the backup folder (conflict resolution)
)

// Which way a change went, from this machine's point of view.
const (
	Up     = "up"     // this machine sent it to the server
	Down   = "down"   // this machine received it from the server
	Local  = "local"  // it happened to this machine's files only
	Remote = "remote" // another machine did it (Machine says which)
)

// Entry is one thing that happened.
type Entry struct {
	Time time.Time `json:"t"`
	Job  string    `json:"job"`
	Op   string    `json:"op"`
	Path string    `json:"p"`
	To   string    `json:"to,omitempty"`
	Dir  bool      `json:"d,omitempty"`
	// Way is Up, Down, Local or Remote.
	Way string `json:"way,omitempty"`
	// Machine names the other machine for Remote entries.
	Machine string `json:"m,omitempty"`
}

// Limits keep the file small: past MaxEntries lines it is cut back to KeepEntries.
const (
	MaxEntries  = 5000
	KeepEntries = 2000
)

// Store is the history file. It is safe for concurrent use inside one process;
// other processes (the CLI) may append to it, as each entry is a single write.
type Store struct {
	Path string

	mu      sync.Mutex
	counted bool // lines is valid
	lines   int  // entries in the file, once counted
}

// Append adds entries, stamping those without a time.
func (s *Store) Append(entries ...Entry) error {
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	var buf []byte
	for _, e := range entries {
		if e.Time.IsZero() {
			e.Time = time.Now()
		}
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf = append(append(buf, b...), '\n')
	}
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(buf)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if !s.counted {
		s.lines, s.counted = s.countLocked(), true
	} else {
		s.lines += len(entries)
	}
	if s.lines > MaxEntries {
		return s.trimLocked()
	}
	return nil
}

func (s *Store) countLocked() int {
	all, _ := s.readAll()
	return len(all)
}

func (s *Store) readAll() ([]Entry, error) {
	f, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Op != "" {
			out = append(out, e) // a torn or foreign line is skipped
		}
	}
	return out, sc.Err()
}

// trimLocked keeps the newest KeepEntries, replacing the file atomically.
func (s *Store) trimLocked() error {
	all, err := s.readAll()
	if err != nil {
		return err
	}
	if len(all) > KeepEntries {
		all = all[len(all)-KeepEntries:]
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), "history.*.tmp")
	if err != nil {
		return err
	}
	w := bufio.NewWriter(tmp)
	for _, e := range all {
		b, _ := json.Marshal(e)
		w.Write(append(b, '\n'))
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	s.lines = len(all)
	return nil
}

// Last returns up to n entries, newest first; job, if set, restricts them.
func (s *Store) Last(n int, job string) ([]Entry, error) {
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, n)
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		if job == "" || all[i].Job == job {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// OfflineRev is the Rev of a history that has never been written.
const OfflineRev = "empty"

// Rev changes whenever the file does; a client passes the one it last showed
// to Wait to be told about the next change.
func (s *Store) Rev() string {
	fi, err := os.Stat(s.Path)
	if err != nil {
		return OfflineRev
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%d/%d", fi.Size(), fi.ModTime().UnixNano())))
	return hex.EncodeToString(sum[:6])
}

// Wait blocks until Rev differs from known, then returns it. It listens for
// file changes (nothing polls) and also watches the folder, so a file that does
// not exist yet is noticed when it appears. The comparison happens after the
// watch is in place, so a change between a client's last look and this call is
// never missed.
func (s *Store) Wait(ctx context.Context, known string) (string, error) {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return "", err
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		return "", err
	}
	for {
		if rev := s.Rev(); rev != known {
			return rev, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err := <-w.Errors:
			return "", err
		case <-w.Events:
		}
	}
}
