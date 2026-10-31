package engine

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/delta"
	"github.com/WentTheFox/FoxCloud/internal/state"
)

// incrEnv builds an engine whose "crypt" remote is really a local folder, so
// Apply runs real rclone commands without a server.
func incrEnv(t *testing.T, role string, maxDelete int) (e *Engine, local, remote string) {
	t.Helper()
	bin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone is not installed")
	}
	local, remote = t.TempDir(), t.TempDir()
	cfg := &config.Config{Jobs: []config.Job{{
		Name: "j", Role: role, LocalPath: local, MaxDelete: maxDelete,
	}}}
	e = &Engine{
		Cfg: cfg, Store: state.Store{Dir: t.TempDir()}, Rclone: bin, Out: io.Discard,
		envFn: func(context.Context) ([]string, error) {
			return []string{
				"RCLONE_CONFIG_FOXCRYPT_TYPE=alias",
				"RCLONE_CONFIG_FOXCRYPT_REMOTE=" + filepath.ToSlash(remote),
			}, nil
		},
	}
	return e, local, remote
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && p != root {
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				rel += "/"
			}
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("tree = %v, want %v", got, want)
	}
}

func TestApplyPushUploadsMovesAndDeletes(t *testing.T) {
	e, local, remote := incrEnv(t, config.RolePush, 5)
	ctx := context.Background()

	write(t, local, "a.txt", "aaa")
	write(t, local, "dir/b.txt", "bbb")
	write(t, local, "dir/deeper/c.txt", "ccc")
	if err := e.Apply(ctx, "j", delta.Ops{
		Uploads: []string{"a.txt", "dir/b.txt", "dir/deeper/c.txt"}, Mkdirs: []string{"dir", "dir/deeper", "empty"},
	}); err != nil {
		t.Fatal(err)
	}
	eq(t, tree(t, remote), "a.txt", "dir/", "dir/b.txt", "dir/deeper/", "dir/deeper/c.txt", "empty/")

	// A move is a server-side rename, not a second upload.
	if err := e.Apply(ctx, "j", delta.Ops{Moves: []delta.Move{{From: "a.txt", To: "moved/a2.txt"}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(remote, "moved", "a2.txt")); string(got) != "aaa" {
		t.Errorf("moved file content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(remote, "a.txt")); err == nil {
		t.Error("the old name is still on the remote")
	}

	// A moved directory too.
	if err := e.Apply(ctx, "j", delta.Ops{Moves: []delta.Move{{From: "dir", To: "renamed", Dir: true}}}); err != nil {
		t.Fatal(err)
	}
	eq(t, tree(t, remote), "empty/", "moved/", "moved/a2.txt", "renamed/", "renamed/b.txt", "renamed/deeper/", "renamed/deeper/c.txt")

	// Deletions: a file, a whole directory, and one that is already gone.
	if err := e.Apply(ctx, "j", delta.Ops{
		DeleteFiles: []string{"moved/a2.txt", "never/was.txt"}, DeleteDirs: []string{"renamed", "ghost"}, Deletes: 4,
	}); err != nil {
		t.Fatal(err)
	}
	eq(t, tree(t, remote), "empty/", "moved/")
}

func TestApplyMoveOfSomethingNeverUploadedUploadsIt(t *testing.T) {
	e, local, remote := incrEnv(t, config.RolePush, 5)
	write(t, local, "new place.txt", "data")
	err := e.Apply(context.Background(), "j", delta.Ops{Moves: []delta.Move{{From: "old place.txt", To: "new place.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, tree(t, remote), "new place.txt")
}

func TestApplyDefersToAFullPassWhenItShould(t *testing.T) {
	e, _, _ := incrEnv(t, config.RolePush, 2)
	ctx := context.Background()
	if err := e.Apply(ctx, "j", delta.Ops{Rescan: true}); err != ErrNeedsFullPass {
		t.Errorf("rescan: err = %v", err)
	}
	if err := e.Apply(ctx, "j", delta.Ops{DeleteFiles: []string{"a", "b", "c"}, Deletes: 3}); err != ErrNeedsFullPass {
		t.Errorf("too many deletes: err = %v", err)
	}
}

func TestApplyRefusesWhenPaused(t *testing.T) {
	e, _, _ := incrEnv(t, config.RolePush, 5)
	e.Store.Save("j", state.Job{Paused: &state.Failure{Kind: "x", Message: "look at it"}})
	if err := e.Apply(context.Background(), "j", delta.Ops{Uploads: []string{"a"}}); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Errorf("err = %v, want a pause", err)
	}
}

func TestApplyBisyncOnlyUploadsAndNeverOverwritesNewerRemoteFiles(t *testing.T) {
	e, local, remote := incrEnv(t, config.RoleBisync, 5)
	ctx := context.Background()

	// The remote copy is newer (another machine wrote it): the upload must leave it.
	write(t, remote, "shared.txt", "from the other machine")
	future := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(remote, "shared.txt"), future, future)
	write(t, local, "shared.txt", "my older edit")
	write(t, local, "mine.txt", "only here")
	write(t, remote, "removed-here.txt", "kept for bisync to decide")

	err := e.Apply(ctx, "j", delta.Ops{
		Uploads:     []string{"shared.txt", "mine.txt"},
		DeleteFiles: []string{"removed-here.txt"}, Deletes: 1,
		Moves: []delta.Move{{From: "gone.txt", To: "mine.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(remote, "shared.txt")); string(got) != "from the other machine" {
		t.Errorf("a newer remote file was overwritten: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(remote, "mine.txt")); string(got) != "only here" {
		t.Errorf("the new file was not uploaded: %q", got)
	}
	if _, err := os.Stat(filepath.Join(remote, "removed-here.txt")); err != nil {
		t.Error("a bisync job must not delete remote files file by file")
	}

	if err := e.Apply(ctx, "j", delta.Ops{Moves: []delta.Move{{From: "d1", To: "d2", Dir: true}}}); err != ErrNeedsFullPass {
		t.Errorf("a directory move under bisync should wait for a full pass, got %v", err)
	}
}

func TestJournalRecordsDescribeWhatWasApplied(t *testing.T) {
	root := t.TempDir()
	write(t, root, "docs/a.txt", "aaa")
	ops := delta.Ops{
		Uploads:     []string{"docs/a.txt", "gone-by-now.txt"},
		Mkdirs:      []string{"docs"},
		Moves:       []delta.Move{{From: "x.txt", To: "y.txt"}, {From: "d1", To: "d2", Dir: true}},
		DeleteFiles: []string{"old.txt"},
		DeleteDirs:  []string{"trash"},
	}
	push := config.Job{Role: config.RolePush, LocalPath: root, RemotePath: "/sub/"}
	var got []string
	for _, r := range JournalRecords(push, ops) {
		got = append(got, r.Op+" "+r.Path+" "+r.To)
	}
	want := []string{
		"mv sub/x.txt sub/y.txt", "mv sub/d1 sub/d2", "del sub/old.txt ", "rmdir sub/trash ",
		"mkdir sub/docs ", "up sub/docs/a.txt ", "up sub/gone-by-now.txt ",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("push records = %q\nwant %q", got, want)
	}

	// Bisync applied only uploads and folders; the log must not claim more.
	bi := config.Job{Role: config.RoleBisync, LocalPath: root}
	got = nil
	for _, r := range JournalRecords(bi, ops) {
		got = append(got, r.Op+" "+r.Path)
	}
	want = []string{"mkdir docs", "up docs/a.txt", "up gone-by-now.txt", "up y.txt"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("bisync records = %q\nwant %q", got, want)
	}

	recs := JournalRecords(push, delta.Ops{Uploads: []string{"docs/a.txt"}})
	if recs[0].Size != 3 || recs[0].Mod == 0 {
		t.Errorf("an upload record should carry the size and time: %+v", recs[0])
	}
}

func TestFetchNeverOverwritesForBisyncButUpdatesForPull(t *testing.T) {
	for _, tc := range []struct {
		role      string
		overwrite bool
		wantLocal string
	}{
		{config.RoleBisync, false, "local edit"},
		{config.RolePull, true, "remote version"},
	} {
		t.Run(tc.role, func(t *testing.T) {
			e, local, remote := incrEnv(t, tc.role, 5)
			backup := t.TempDir()
			job := e.Cfg.Jobs[0]
			job.BackupDir = backup
			e.Cfg.Jobs[0] = job

			write(t, remote, "new.txt", "brand new")
			write(t, remote, "shared.txt", "remote version")
			future := time.Now().Add(time.Hour)
			os.Chtimes(filepath.Join(remote, "shared.txt"), future, future)
			write(t, local, "shared.txt", "local edit")
			past := time.Now().Add(-time.Hour)
			os.Chtimes(filepath.Join(local, "shared.txt"), past, past)

			if err := e.Fetch(context.Background(), "j", []string{"new.txt", "shared.txt", "missing-on-remote.txt"}, tc.overwrite); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(filepath.Join(local, "new.txt")); string(got) != "brand new" {
				t.Errorf("the new file was not fetched: %q", got)
			}
			if got, _ := os.ReadFile(filepath.Join(local, "shared.txt")); string(got) != tc.wantLocal {
				t.Errorf("shared.txt = %q, want %q", got, tc.wantLocal)
			}
			if tc.overwrite {
				entries, _ := os.ReadDir(backup)
				if len(entries) != 1 {
					t.Errorf("the replaced file should be moved to the backup folder, found %d entries", len(entries))
				}
			}
		})
	}
}

func TestFetchRefusesOtherRoles(t *testing.T) {
	e, _, _ := incrEnv(t, config.RolePush, 5)
	if err := e.Fetch(context.Background(), "j", []string{"a"}, false); err == nil {
		t.Error("a push job does not download")
	}
}
