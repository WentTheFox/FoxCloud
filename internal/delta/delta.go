// Package delta works out what a handful of file system events mean for the
// remote: which files to upload, which to delete, and which were really moved.
// It keeps an in-memory picture of the synced tree (paths with size and
// modification time) so that a vanished path can be told apart from a renamed
// one, which turns a rename of a large folder into one server-side move
// instead of a delete and a full re-upload.
package delta

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/filter"
)

// Entry is what is remembered about one path.
type Entry struct {
	Size    int64
	ModTime time.Time
	Dir     bool
}

// Move is a rename seen on disk, with paths relative to the sync root.
type Move struct {
	From, To string
	Dir      bool
}

// Ops is what to do on the remote. All paths are relative to the sync root and
// use forward slashes.
type Ops struct {
	Uploads     []string // new or changed files
	Created     []string // the Uploads that did not exist before, sorted
	Mkdirs      []string // new directories (including empty ones)
	Moves       []Move
	DeleteFiles []string
	DeleteDirs  []string // top-most removed directories; everything below goes with them
	// Deletes is how many files the deletions amount to, for the max_delete check.
	Deletes int
	// Rescan means events were lost: nothing here can be trusted and a full
	// pass is needed.
	Rescan bool
}

// Empty reports whether there is nothing to do.
func (o Ops) Empty() bool {
	return !o.Rescan && len(o.Uploads) == 0 && len(o.Mkdirs) == 0 && len(o.Moves) == 0 &&
		len(o.DeleteFiles) == 0 && len(o.DeleteDirs) == 0
}

// Index is the remembered tree. It is not safe for concurrent use.
type Index struct {
	root   string
	filter *filter.Matcher
	m      map[string]Entry
}

// Scan reads the tree under root. Paths the filter excludes are left out, as
// they are never synced.
func Scan(root string, f *filter.Matcher) (*Index, error) {
	ix := &Index{root: filepath.Clean(root), filter: f, m: map[string]Entry{}}
	if _, err := ix.addTree(ix.root); err != nil {
		return nil, err
	}
	return ix, nil
}

// Len is how many paths are remembered.
func (ix *Index) Len() int { return len(ix.m) }

func (ix *Index) rel(abs string) (string, bool) {
	r, err := filepath.Rel(ix.root, abs)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(r), true
}

func (ix *Index) included(rel string, dir bool) bool {
	if dir {
		return ix.filter.IncludeDir(rel)
	}
	return ix.filter.Include(rel)
}

// addTree records abs and everything below it, and returns the new paths in
// walk order. Unreadable entries are skipped: they will not sync either.
func (ix *Index) addTree(abs string) ([]string, error) {
	var added []string
	err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == abs {
				return err
			}
			return nil
		}
		rel, ok := ix.rel(p)
		if !ok {
			return nil // the root itself
		}
		if d.IsDir() {
			if !ix.included(rel, true) {
				return filepath.SkipDir
			}
			ix.m[rel] = Entry{Dir: true}
			added = append(added, rel)
			return nil
		}
		if !d.Type().IsRegular() || !ix.included(rel, false) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		ix.m[rel] = Entry{Size: fi.Size(), ModTime: fi.ModTime()}
		added = append(added, rel)
		return nil
	})
	return added, err
}

func sameFile(a, b Entry) bool {
	return !a.Dir && !b.Dir && a.Size == b.Size && a.ModTime.Truncate(time.Millisecond).Equal(b.ModTime.Truncate(time.Millisecond))
}

// Update takes the paths the watcher saw change (absolute) and returns what
// the remote needs. The index is brought up to date as it goes. overflow means
// the watcher lost events.
func (ix *Index) Update(touched []string, overflow bool) Ops {
	if overflow {
		return Ops{Rescan: true}
	}
	goneFiles := map[string]Entry{}
	goneDirs := map[string]bool{}
	newFiles := map[string]Entry{}
	newDirs := map[string]bool{}
	changed := map[string]bool{} // existing files whose content changed

	for _, abs := range touched {
		rel, ok := ix.rel(abs)
		if !ok {
			continue
		}
		fi, err := os.Lstat(abs)
		switch {
		case err != nil:
			old, known := ix.m[rel]
			if !known {
				continue
			}
			if old.Dir {
				goneDirs[rel] = true
				prefix := rel + "/"
				for p, e := range ix.m {
					if strings.HasPrefix(p, prefix) {
						if !e.Dir {
							goneFiles[p] = e
						}
						delete(ix.m, p)
					}
				}
			} else {
				goneFiles[rel] = old
			}
			delete(ix.m, rel)
			// It may have been reported as new earlier in this same batch.
			delete(newFiles, rel)
			delete(newDirs, rel)
		case fi.IsDir():
			if _, known := ix.m[rel]; known || !ix.included(rel, true) {
				continue
			}
			added, _ := ix.addTree(abs)
			for _, p := range added {
				if ix.m[p].Dir {
					newDirs[p] = true
				} else {
					newFiles[p] = ix.m[p]
				}
			}
		case fi.Mode().IsRegular():
			if !ix.included(rel, false) {
				continue
			}
			cur := Entry{Size: fi.Size(), ModTime: fi.ModTime()}
			old, known := ix.m[rel]
			switch {
			case !known:
				newFiles[rel] = cur
			case !sameFile(old, cur):
				changed[rel] = true
			default:
				continue
			}
			ix.m[rel] = cur
		}
	}

	var ops Ops

	// Whole directories moved: every file under the old one is under the new
	// one with the same relative path, size and time.
	for _, from := range sortedKeys(goneDirs) {
		if _, still := goneDirs[from]; !still {
			continue
		}
		for _, to := range sortedKeys(newDirs) {
			if ix.dirMatches(from, to, goneFiles, newFiles) {
				ops.Moves = append(ops.Moves, Move{From: from, To: to, Dir: true})
				for p := range goneFiles {
					if strings.HasPrefix(p, from+"/") {
						delete(goneFiles, p)
					}
				}
				for p := range newFiles {
					if strings.HasPrefix(p, to+"/") {
						delete(newFiles, p)
					}
				}
				delete(goneDirs, from)
				for d := range newDirs {
					if d == to || strings.HasPrefix(d, to+"/") {
						delete(newDirs, d)
					}
				}
				break
			}
		}
	}

	// Single files moved: a vanished file and a new file that look the same,
	// each the only one of its kind (two look-alikes would be guesswork).
	type sig struct {
		size int64
		mod  int64
	}
	sigOf := func(e Entry) sig { return sig{e.Size, e.ModTime.Truncate(time.Millisecond).UnixNano()} }
	goneBy, newBy := map[sig][]string{}, map[sig][]string{}
	for p, e := range goneFiles {
		goneBy[sigOf(e)] = append(goneBy[sigOf(e)], p)
	}
	for p, e := range newFiles {
		newBy[sigOf(e)] = append(newBy[sigOf(e)], p)
	}
	for k, from := range goneBy {
		if to := newBy[k]; len(from) == 1 && len(to) == 1 {
			ops.Moves = append(ops.Moves, Move{From: from[0], To: to[0]})
			delete(goneFiles, from[0])
			delete(newFiles, to[0])
		}
	}
	sort.Slice(ops.Moves, func(i, j int) bool { return ops.Moves[i].From < ops.Moves[j].From })

	for p := range newFiles {
		ops.Uploads = append(ops.Uploads, p)
		ops.Created = append(ops.Created, p)
	}
	sort.Strings(ops.Created)
	for p := range changed {
		ops.Uploads = append(ops.Uploads, p)
	}
	sort.Strings(ops.Uploads)
	ops.Mkdirs = sortedKeys(newDirs)

	// Only the top-most removed directories: the rest go with them.
	for _, d := range sortedKeys(goneDirs) {
		nested := false
		for _, other := range ops.DeleteDirs {
			if strings.HasPrefix(d, other+"/") {
				nested = true
				break
			}
		}
		if !nested {
			ops.DeleteDirs = append(ops.DeleteDirs, d)
		}
	}
	// Every vanished file counts towards max_delete, but those inside a removed
	// directory are deleted with it rather than one by one.
	ops.Deletes = len(goneFiles)
	for _, p := range sortedKeys(goneFiles) {
		inside := false
		for _, d := range ops.DeleteDirs {
			if strings.HasPrefix(p, d+"/") {
				inside = true
				break
			}
		}
		if !inside {
			ops.DeleteFiles = append(ops.DeleteFiles, p)
		}
	}
	return ops
}

// dirMatches reports whether directory from became directory to: both have
// files, and each file under from has a twin under to.
func (ix *Index) dirMatches(from, to string, gone, added map[string]Entry) bool {
	fromPrefix, toPrefix := from+"/", to+"/"
	count := 0
	for p, e := range gone {
		if !strings.HasPrefix(p, fromPrefix) {
			continue
		}
		twin, ok := added[toPrefix+strings.TrimPrefix(p, fromPrefix)]
		if !ok || !sameFile(e, twin) {
			return false
		}
		count++
	}
	if count == 0 {
		return false
	}
	for p := range added {
		if strings.HasPrefix(p, toPrefix) {
			count--
		}
	}
	return count == 0
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
