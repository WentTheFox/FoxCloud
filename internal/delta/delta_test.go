package delta

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/filter"
)

func put(t *testing.T, root, rel, body string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

var t0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func scan(t *testing.T, root string) *Index {
	t.Helper()
	ix, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestNewAndChangedFilesAreUploaded(t *testing.T) {
	root := t.TempDir()
	changed := put(t, root, "a.txt", "one", t0)
	put(t, root, "keep.txt", "k", t0)
	ix := scan(t, root)

	put(t, root, "a.txt", "two!", t0.Add(time.Hour))
	fresh := put(t, root, "d/new.txt", "n", t0)
	ops := ix.Update([]string{changed, fresh, filepath.Join(root, "d")}, false)

	if !reflect.DeepEqual(ops.Uploads, []string{"a.txt", "d/new.txt"}) {
		t.Errorf("uploads = %v", ops.Uploads)
	}
	if !reflect.DeepEqual(ops.Mkdirs, []string{"d"}) {
		t.Errorf("mkdirs = %v", ops.Mkdirs)
	}
	// An event with nothing behind it (touched, not changed) is no work.
	if ops := ix.Update([]string{filepath.Join(root, "keep.txt")}, false); !ops.Empty() {
		t.Errorf("an unchanged file produced %+v", ops)
	}
}

func TestDeletedFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	gone := put(t, root, "gone.txt", "g", t0)
	put(t, root, "dir/a", "aa", t0)
	put(t, root, "dir/sub/b", "bbb", t0)
	ix := scan(t, root)

	os.Remove(gone)
	os.RemoveAll(filepath.Join(root, "dir"))
	ops := ix.Update([]string{gone, filepath.Join(root, "dir"), filepath.Join(root, "dir", "a")}, false)

	if !reflect.DeepEqual(ops.DeleteFiles, []string{"gone.txt"}) {
		t.Errorf("delete files = %v", ops.DeleteFiles)
	}
	if !reflect.DeepEqual(ops.DeleteDirs, []string{"dir"}) {
		t.Errorf("delete dirs = %v", ops.DeleteDirs)
	}
	if ops.Deletes != 3 {
		t.Errorf("deletes = %d, want 3 (every file counts towards max_delete)", ops.Deletes)
	}
	if ix.Len() != 0 {
		t.Errorf("the index still holds %d paths", ix.Len())
	}
}

func TestRenamedFileIsAMoveNotDeleteAndUpload(t *testing.T) {
	root := t.TempDir()
	old := put(t, root, "old name.txt", "same content", t0)
	ix := scan(t, root)

	renamed := filepath.Join(root, "sub", "new name.txt")
	os.MkdirAll(filepath.Dir(renamed), 0o755)
	os.Rename(old, renamed)
	ops := ix.Update([]string{old, renamed, filepath.Dir(renamed)}, false)

	want := []Move{{From: "old name.txt", To: "sub/new name.txt"}}
	if !reflect.DeepEqual(ops.Moves, want) {
		t.Errorf("moves = %+v, want %+v", ops.Moves, want)
	}
	if len(ops.Uploads) != 0 || len(ops.DeleteFiles) != 0 {
		t.Errorf("a move must not also upload or delete: %+v", ops)
	}
}

func TestTwoIdenticalLookingFilesAreNotGuessedAsMoves(t *testing.T) {
	root := t.TempDir()
	a := put(t, root, "a", "same", t0)
	b := put(t, root, "b", "same", t0)
	ix := scan(t, root)

	os.Remove(a)
	os.Remove(b)
	c := put(t, root, "c", "same", t0)
	d := put(t, root, "d", "same", t0)
	ops := ix.Update([]string{a, b, c, d}, false)

	if len(ops.Moves) != 0 {
		t.Errorf("ambiguous look-alikes were paired: %+v", ops.Moves)
	}
	if len(ops.DeleteFiles) != 2 || len(ops.Uploads) != 2 {
		t.Errorf("ops = %+v", ops)
	}
}

func TestRenamedDirectoryIsOneMove(t *testing.T) {
	root := t.TempDir()
	put(t, root, "proj/a.txt", "aaa", t0)
	put(t, root, "proj/deep/b.txt", "bb", t0.Add(time.Minute))
	ix := scan(t, root)

	from, to := filepath.Join(root, "proj"), filepath.Join(root, "archive", "proj-2026")
	os.MkdirAll(filepath.Dir(to), 0o755)
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	ops := ix.Update([]string{from, to, filepath.Join(root, "archive")}, false)

	want := []Move{{From: "proj", To: "archive/proj-2026", Dir: true}}
	if !reflect.DeepEqual(ops.Moves, want) {
		t.Errorf("moves = %+v, want %+v", ops.Moves, want)
	}
	if len(ops.Uploads) != 0 || len(ops.DeleteFiles) != 0 || len(ops.DeleteDirs) != 0 || ops.Deletes != 0 {
		t.Errorf("a directory move must not also upload or delete: %+v", ops)
	}
	if !reflect.DeepEqual(ops.Mkdirs, []string{"archive"}) {
		t.Errorf("mkdirs = %v (only the new parent)", ops.Mkdirs)
	}
}

func TestFilteredPathsAreIgnored(t *testing.T) {
	root := t.TempDir()
	f, err := filter.Load(func() string {
		p := filepath.Join(t.TempDir(), "f")
		os.WriteFile(p, []byte("- *.tmp\n- cache/\n"), 0o644)
		return p
	}())
	if err != nil {
		t.Fatal(err)
	}
	ix, err := Scan(root, f)
	if err != nil {
		t.Fatal(err)
	}
	tmp := put(t, root, "x.tmp", "t", t0)
	inCache := put(t, root, "cache/c", "c", t0)
	real := put(t, root, "real.txt", "r", t0)
	ops := ix.Update([]string{tmp, inCache, filepath.Join(root, "cache"), real}, false)
	if !reflect.DeepEqual(ops.Uploads, []string{"real.txt"}) || len(ops.Mkdirs) != 0 {
		t.Errorf("ops = %+v", ops)
	}
}

func TestOverflowAsksForARescan(t *testing.T) {
	ix := scan(t, t.TempDir())
	if ops := ix.Update(nil, true); !ops.Rescan {
		t.Error("lost events must request a full pass")
	}
}

func TestPathsOutsideTheRootAreIgnored(t *testing.T) {
	root := t.TempDir()
	ix := scan(t, root)
	other := put(t, t.TempDir(), "x", "x", t0)
	if ops := ix.Update([]string{other, root}, false); !ops.Empty() {
		t.Errorf("ops = %+v", ops)
	}
}
