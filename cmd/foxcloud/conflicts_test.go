package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WentTheFox/FoxCloud/internal/config"
)

func conflictFixture(t *testing.T) (*config.Config, string, string) {
	t.Helper()
	root, backup := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"a.txt": "mine", "a.txt.conflict1": "theirs",
		"b.txt": "keep", "b.txt.conflict1": "other",
		"c.txt": "same", "c.txt.conflict1": "same",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Jobs: []config.Job{{Name: "j", Role: config.RoleBisync, LocalPath: root, BackupDir: backup}}}
	return cfg, root, backup
}

func has(t *testing.T, dir, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestResolveWalksThroughConflicts(t *testing.T) {
	cfg, root, backup := conflictFixture(t)
	// a.txt: take copy 1; b.txt: a wrong answer, then skip; c.txt: keep both.
	var out bytes.Buffer
	in := strings.NewReader("1\nwhat\ns\nb\n\n")
	if err := resolveConflicts(cfg, "", in, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "theirs" || has(t, root, "a.txt.conflict1") {
		t.Errorf("a.txt should now hold copy 1: %q", got)
	}
	if !has(t, root, "b.txt.conflict1") {
		t.Error("skipped conflict must stay")
	}
	if !has(t, root, "c (conflict 1).txt") || has(t, root, "c.txt.conflict1") {
		t.Error("keep both should rename the copy")
	}
	if !has(t, backup, "conflicts") {
		t.Error("the replaced original should be archived")
	}
	for _, want := range []string{"[j 1/3] a.txt", "Not an option.", "Resolved 2, left 1."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestResolveQuitLeavesTheRest(t *testing.T) {
	cfg, root, _ := conflictFixture(t)
	var out bytes.Buffer
	if err := resolveConflicts(cfg, "", strings.NewReader("q\n\n"), &out); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.txt.conflict1", "b.txt.conflict1", "c.txt.conflict1"} {
		if !has(t, root, n) {
			t.Errorf("%s should be untouched", n)
		}
	}
	if !strings.Contains(out.String(), "Resolved 0, left 3.") {
		t.Errorf("summary missing:\n%s", out.String())
	}
}

func TestResolveEndOfInputIsAQuit(t *testing.T) {
	cfg, root, _ := conflictFixture(t)
	if err := resolveConflicts(cfg, "", strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !has(t, root, "a.txt.conflict1") {
		t.Error("nothing should change without answers")
	}
}

func TestResolveWithNoConflicts(t *testing.T) {
	cfg, root, _ := conflictFixture(t)
	for _, n := range []string{"a.txt.conflict1", "b.txt.conflict1", "c.txt.conflict1"} {
		os.Remove(filepath.Join(root, n))
	}
	var out bytes.Buffer
	if err := resolveConflicts(cfg, "", strings.NewReader("\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No conflicts.") {
		t.Errorf("got %q", out.String())
	}
}
