package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/history"
	"github.com/WentTheFox/FoxCloud/internal/state"
)

func historyStore() (*history.Store, error) {
	dir, err := state.Dir()
	if err != nil {
		return nil, err
	}
	return &history.Store{Path: filepath.Join(dir, "history.jsonl")}, nil
}

// historyCmd prints what has happened to the files, newest first.
func historyCmd(args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	n := fs.Int("n", 30, "how many entries to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := historyStore()
	if err != nil {
		return err
	}
	job := ""
	if fs.NArg() > 0 {
		job = fs.Arg(0)
	}
	entries, err := st.Last(*n, job)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("Nothing has changed yet.")
		return nil
	}
	for _, e := range entries {
		fmt.Println(formatEntry(e, time.Now()))
	}
	return nil
}

// historyJSONCmd prints the history as JSON for the widget; with -wait it
// blocks until the history differs from a revision the caller already showed.
func historyJSONCmd(args []string) error {
	fs := flag.NewFlagSet("history-json", flag.ContinueOnError)
	n := fs.Int("n", 50, "how many entries to include")
	wait := fs.String("wait", "", "block until the history differs from this revision")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := historyStore()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rev := st.Rev()
	if *wait != "" {
		if rev, err = st.Wait(ctx, *wait); err != nil {
			return err
		}
	}
	entries, err := st.Last(*n, "")
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []history.Entry{}
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Rev     string          `json:"rev"`
		Entries []history.Entry `json:"entries"`
	}{rev, entries})
}

// formatEntry is one line of `foxcloud history`.
func formatEntry(e history.Entry, now time.Time) string {
	t := e.Time.Local()
	when := t.Format("15:04")
	if t.YearDay() != now.YearDay() || t.Year() != now.Year() {
		when = t.Format("01-02 15:04")
	}
	what := e.Path
	if e.Op == history.OpMoved && e.To != "" {
		what += " -> " + e.To
	}
	if e.Dir {
		what += "/"
	}
	via := e.Job
	switch e.Way {
	case history.Up:
		via += " ↑"
	case history.Down:
		via += " ↓"
	case history.Remote:
		via += " ← " + e.Machine
	}
	return fmt.Sprintf("%-11s %-9s %s  (%s)", when, e.Op, what, via)
}
