package conflict

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func fixture(t *testing.T) (*Resolver, string) {
	root, archive := t.TempDir(), t.TempDir()
	write(t, root, "docs/a.txt", "mine")
	write(t, root, "docs/a.txt.conflict1", "theirs")
	write(t, root, "docs/a.txt.conflict2", "older")
	write(t, root, "b.txt", "same")
	write(t, root, "b.txt.conflict1", "same")
	write(t, root, "gone.txt.conflict1", "orphan")
	write(t, root, "notes.conflictual", "not a conflict")
	return &Resolver{Root: root, Archive: archive, Now: func() time.Time {
		return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	}}, root
}

func TestScanGroupsVariantsWithTheirOriginal(t *testing.T) {
	_, root := fixture(t)
	got, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 conflicts, got %+v", got)
	}
	a, _ := Find(got, "docs/a.txt")
	if !a.Exists || len(a.Variants) != 2 || a.Variants[0].N != 1 || a.Variants[1].N != 2 {
		t.Errorf("docs/a.txt: %+v", a)
	}
	if a.Original.Size != 4 || a.Variants[0].Size != 6 {
		t.Errorf("sizes: %+v", a)
	}
	g, _ := Find(got, "gone.txt")
	if g.Exists || len(g.Variants) != 1 {
		t.Errorf("orphan should have no original: %+v", g)
	}
	if _, ok := Find(got, "notes"); ok {
		t.Error("notes.conflictual is not a conflict copy")
	}
}

func TestScanHonoursTheFilter(t *testing.T) {
	_, root := fixture(t)
	got, _ := Scan(root, func(rel string) bool { return !strings.HasPrefix(rel, "docs/") })
	if _, ok := Find(got, "docs/a.txt"); ok {
		t.Error("filtered-out folder was reported")
	}
	if len(got) != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestKeepOriginalArchivesTheCopies(t *testing.T) {
	r, root := fixture(t)
	cs, _ := Scan(root, nil)
	c, _ := Find(cs, "docs/a.txt")
	if _, err := r.KeepOriginal(c); err != nil {
		t.Fatal(err)
	}
	if read(t, root, "docs/a.txt") != "mine" || exists(root, "docs/a.txt.conflict1") || exists(root, "docs/a.txt.conflict2") {
		t.Error("original should stay and copies go")
	}
	if got := read(t, r.Archive, "20261031-120000/docs/a.txt.conflict1"); got != "theirs" {
		t.Errorf("archived copy = %q", got)
	}
}

func TestKeepOriginalRefusesWithoutAnOriginal(t *testing.T) {
	r, root := fixture(t)
	cs, _ := Scan(root, nil)
	c, _ := Find(cs, "gone.txt")
	if _, err := r.KeepOriginal(c); err == nil {
		t.Error("expected an error")
	}
	if !exists(root, "gone.txt.conflict1") {
		t.Error("nothing should have moved")
	}
}

func TestKeepVariantReplacesTheOriginalAndArchivesTheRest(t *testing.T) {
	r, root := fixture(t)
	cs, _ := Scan(root, nil)
	c, _ := Find(cs, "docs/a.txt")
	if _, err := r.KeepVariant(c, 1); err != nil {
		t.Fatal(err)
	}
	if read(t, root, "docs/a.txt") != "theirs" || exists(root, "docs/a.txt.conflict1") || exists(root, "docs/a.txt.conflict2") {
		t.Error("variant 1 should now be the file")
	}
	if read(t, r.Archive, "20261031-120000/docs/a.txt") != "mine" || read(t, r.Archive, "20261031-120000/docs/a.txt.conflict2") != "older" {
		t.Error("displaced files should be archived")
	}
	if _, err := r.KeepVariant(c, 9); err == nil {
		t.Error("unknown number should fail")
	}
}

func TestKeepVariantWorksWithoutAnOriginal(t *testing.T) {
	r, root := fixture(t)
	cs, _ := Scan(root, nil)
	c, _ := Find(cs, "gone.txt")
	if _, err := r.KeepVariant(c, 1); err != nil {
		t.Fatal(err)
	}
	if read(t, root, "gone.txt") != "orphan" {
		t.Error("the copy should become the file")
	}
}

func TestKeepBothRenamesWithoutLosingAnything(t *testing.T) {
	r, root := fixture(t)
	cs, _ := Scan(root, nil)
	c, _ := Find(cs, "docs/a.txt")
	if _, err := r.KeepBoth(c); err != nil {
		t.Fatal(err)
	}
	if read(t, root, "docs/a.txt") != "mine" ||
		read(t, root, "docs/a (conflict 1).txt") != "theirs" ||
		read(t, root, "docs/a (conflict 2).txt") != "older" {
		t.Error("all versions should remain under distinct names")
	}
	after, _ := Scan(root, nil)
	if _, ok := Find(after, "docs/a.txt"); ok {
		t.Error("no longer a conflict")
	}
}

func TestCleanArchivesOnlyIdenticalCopies(t *testing.T) {
	r, root := fixture(t)
	cs, _ := Scan(root, nil)
	done, err := r.Clean(cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || exists(root, "b.txt.conflict1") {
		t.Errorf("only b.txt.conflict1 is identical: %v", done)
	}
	if !exists(root, "docs/a.txt.conflict1") || !exists(root, "gone.txt.conflict1") {
		t.Error("differing copies must stay")
	}
}

func TestPathsOutsideTheFolderAreRefused(t *testing.T) {
	r, _ := fixture(t)
	c := Conflict{Exists: true, Original: File{Path: "x"}, Variants: []Variant{{File{Path: "../evil.conflict1"}, 1}}}
	if _, err := r.KeepOriginal(c); err == nil {
		t.Error("a path escaping the root must be refused")
	}
}

func TestSameContent(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", 200<<10)
	write(t, root, "a", big)
	write(t, root, "b", big)
	write(t, root, "c", big[:len(big)-1]+"y")
	if same, err := SameContent(filepath.Join(root, "a"), filepath.Join(root, "b")); err != nil || !same {
		t.Errorf("a,b: %v %v", same, err)
	}
	if same, _ := SameContent(filepath.Join(root, "a"), filepath.Join(root, "c")); same {
		t.Error("a,c differ in the last byte")
	}
}
