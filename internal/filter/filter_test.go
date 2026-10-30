package filter

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "filters.txt")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// rules is the project's own filter file plus the other pattern shapes rclone
// supports, so the cross-check below covers each of them.
const rules = `# comments and blanks are fine

- desktop.ini
- Thumbs.db
- ~$*
- *.tmp
- .stfolder/**
- .Trash-*/**
- /build/
- /only-root.log
- cache/
- *.{bak,swp}
- report-[0-9].txt
- notes?.md
+ keep/important.tmp
- node_modules/
`

var paths = []string{
	"a.txt", "desktop.ini", "sub/desktop.ini", "Thumbs.db", "~$doc.docx", "sub/~$doc.docx",
	"x.tmp", "sub/y.tmp", ".stfolder/s", ".stfolder/deep/s", ".Trash-1000/files/f",
	"build/out.bin", "sub/build/out.bin", "only-root.log", "sub/only-root.log",
	"cache/c1", "sub/cache/c1", "sub/cache/deeper/c2", "a.bak", "sub/b.swp", "c.txt.bak2",
	"report-1.txt", "report-12.txt", "report-a.txt", "notes1.md", "notes12.md", "notes.md",
	"keep/important.tmp", "keep/other.tmp", "keep/fine.txt", "node_modules/x/y.js",
	"src/node_modules/z.js", "readme.md", "deep/er/est/file.txt",
}

func TestKnownDecisions(t *testing.T) {
	m, err := Load(write(t, rules))
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{
		"a.txt": true, "desktop.ini": false, "sub/desktop.ini": false, "~$doc.docx": false,
		"x.tmp": false, "build/out.bin": false, "sub/build/out.bin": true,
		"only-root.log": false, "sub/only-root.log": true, "sub/cache/deeper/c2": false,
		"a.bak": false, "report-1.txt": false, "report-12.txt": true,
		"notes1.md": false, "notes12.md": true, "src/node_modules/z.js": false,
		"keep/important.tmp": false, // the exclude for *.tmp comes first and wins
	} {
		if got := m.Include(p); got != want {
			t.Errorf("Include(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestEmptyAndNilIncludeEverything(t *testing.T) {
	var nilM *Matcher
	empty, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []*Matcher{nilM, empty} {
		if !m.Include("any/thing.tmp") || !m.IncludeDir("any") {
			t.Error("no rules must include everything")
		}
	}
}

func TestBadRules(t *testing.T) {
	for _, body := range []string{"nonsense\n", "* x\n", "- [abc\n", "- {a,b\n"} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%q should be rejected", body)
		}
	}
}

// The matcher must agree with rclone itself: build a tree, ask rclone which
// files its filter keeps, and compare path by path.
func TestAgreesWithRclone(t *testing.T) {
	bin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone is not installed")
	}
	root := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file := write(t, rules)
	out, err := exec.Command(bin, "lsf", "-R", "--files-only", "--filter-from", file, root).Output()
	if err != nil {
		t.Fatalf("rclone lsf: %v", err)
	}
	kept := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		kept[strings.TrimSpace(l)] = true
	}

	m, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	var wrong []string
	for _, p := range paths {
		if m.Include(p) != kept[p] {
			wrong = append(wrong, p)
		}
	}
	sort.Strings(wrong)
	if len(wrong) > 0 {
		t.Errorf("disagrees with rclone on: %v", wrong)
	}
}
