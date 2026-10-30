package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ExecDir is the folder holding the running executable, symlinks resolved:
// where an update is installed.
func ExecDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// Staged is a release that has been downloaded and checked and is ready to
// install. Close it to delete the staging folder.
type Staged struct {
	tmp   string
	files map[string]string
}

// Prepare downloads the release and checks it (checksum, and the signature on
// Windows) without touching any installed file, so the slow part can happen
// while the program keeps working.
func Prepare(ctx context.Context, r Release) (*Staged, error) {
	tmp, err := os.MkdirTemp("", "foxcloud-update-")
	if err != nil {
		return nil, err
	}
	s := &Staged{tmp: tmp}
	archive, err := Download(ctx, r, tmp)
	if err == nil {
		s.files, err = Extract(archive, tmp)
	}
	if err == nil {
		err = VerifySignatures(s.files)
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Install puts the staged executables in dir, keeping the old ones as *.old.
func (s *Staged) Install(dir string) error { return Install(dir, s.files) }

// Close deletes the staging folder.
func (s *Staged) Close() { os.RemoveAll(s.tmp) }

// Apply is Prepare then Install.
func Apply(ctx context.Context, r Release, dir string) error {
	s, err := Prepare(ctx, r)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Install(dir)
}

// OldEnough reports whether a release has been public for at least delay, so
// auto mode does not take a release the moment it appears. A release whose age
// is unknown is never old enough.
func OldEnough(r Release, delay time.Duration, now time.Time) bool {
	if r.Published.IsZero() {
		return false
	}
	return now.Sub(r.Published) >= delay
}

// Rollback puts the executables saved by the last Install back, and returns
// the names it restored. The replaced files are left as *.new (Windows cannot
// delete a running executable) and removed when possible.
func Rollback(dir string) ([]string, error) {
	var restored []string
	for _, b := range allBinaries() {
		cur := filepath.Join(dir, exeName(b))
		old := cur + ".old"
		if _, err := os.Stat(old); err != nil {
			continue
		}
		spare := cur + ".new"
		os.Remove(spare)
		if err := os.Rename(cur, spare); err != nil && !errors.Is(err, os.ErrNotExist) {
			return restored, err
		}
		if err := os.Rename(old, cur); err != nil {
			os.Rename(spare, cur) // keep what was there
			return restored, err
		}
		os.Remove(spare)
		restored = append(restored, exeName(b))
	}
	if len(restored) == 0 {
		return nil, fmt.Errorf("no previous version is saved in %s", dir)
	}
	return restored, nil
}
