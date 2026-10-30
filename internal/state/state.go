// Package state persists per-job sync state so it survives restarts.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type Failure struct {
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

type Job struct {
	LastAttempt time.Time `json:"lastAttempt"`
	LastSuccess time.Time `json:"lastSuccess"`
	LastFailure *Failure  `json:"lastFailure,omitempty"`
	// Paused is set when a human has to resolve something before syncing resumes.
	Paused *Failure `json:"paused,omitempty"`
	// Resynced records that bisync has completed its initial --resync.
	Resynced bool `json:"resynced"`
}

// Dir returns the directory state files live in.
func Dir() (string, error) {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("LOCALAPPDATA is not set")
		}
		return filepath.Join(base, "FoxCloud"), nil
	}
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "foxcloud"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "foxcloud"), nil
}

// Store reads and writes one JSON file per job.
type Store struct{ Dir string }

func (s Store) path(job string) string { return filepath.Join(s.Dir, job+".json") }

// Load returns the saved state, or an empty state if there is none yet.
func (s Store) Load(job string) (Job, error) {
	var j Job
	b, err := os.ReadFile(s.path(job))
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return j, err
	}
	return j, json.Unmarshal(b, &j)
}

// Save writes the state atomically, so a crash never leaves a torn file.
func (s Store) Save(job string, j Job) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, job+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	return os.Rename(tmp.Name(), s.path(job))
}
