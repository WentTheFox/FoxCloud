// Package conflict finds and resolves the files rclone bisync leaves behind
// when both sides changed the same file: it keeps one version under the real
// name and renames the other to `name.conflict1`, `name.conflict2`, and so on.
// Nothing here deletes data: every file a resolution displaces is moved into an
// archive folder.
package conflict

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// suffix matches the names bisync gives its losing versions.
var suffix = regexp.MustCompile(`^(.+)\.conflict([0-9]+)$`)

// IsCopyName reports whether a file name is a bisync conflict copy.
func IsCopyName(name string) bool { return suffix.MatchString(name) }

// File describes one version of a file.
type File struct {
	Path    string    `json:"path"` // slash-separated, relative to the job's folder
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

// Variant is a conflict copy and its number (the 1 of `.conflict1`).
type Variant struct {
	File
	N int `json:"n"`
}

// Conflict is one file with at least one conflict copy next to it.
type Conflict struct {
	// Original is the file under its real name. Exists is false when only
	// conflict copies are left (the real file was deleted on one side).
	Original File      `json:"original"`
	Exists   bool      `json:"exists"`
	Variants []Variant `json:"variants"`
}

// Scan walks root and returns its conflicts sorted by path. include, if set,
// decides which relative paths count, so files a job's filter excludes are not
// reported.
func Scan(root string, include func(rel string) bool) ([]Conflict, error) {
	byOriginal := map[string]*Conflict{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // vanished mid-walk
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		m := suffix.FindStringSubmatch(d.Name())
		if m == nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if include != nil && !include(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		n, _ := strconv.Atoi(m[2])
		orig := path.Join(path.Dir(rel), m[1])
		c := byOriginal[orig]
		if c == nil {
			c = &Conflict{Original: File{Path: orig}}
			if oi, err := os.Stat(filepath.Join(root, filepath.FromSlash(orig))); err == nil && oi.Mode().IsRegular() {
				c.Exists = true
				c.Original.Size, c.Original.ModTime = oi.Size(), oi.ModTime()
			}
			byOriginal[orig] = c
		}
		c.Variants = append(c.Variants, Variant{File{rel, info.Size(), info.ModTime()}, n})
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Conflict, 0, len(byOriginal))
	for _, c := range byOriginal {
		sort.Slice(c.Variants, func(i, j int) bool { return c.Variants[i].N < c.Variants[j].N })
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Original.Path < out[j].Original.Path })
	return out, nil
}

// Find returns the conflict whose original is rel (slash-separated).
func Find(conflicts []Conflict, rel string) (Conflict, bool) {
	rel = path.Clean(filepath.ToSlash(rel))
	for _, c := range conflicts {
		if c.Original.Path == rel {
			return c, true
		}
	}
	return Conflict{}, false
}

// SameContent reports whether two files hold the same bytes.
func SameContent(a, b string) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	ia, err := fa.Stat()
	if err != nil {
		return false, err
	}
	ib, err := fb.Stat()
	if err != nil {
		return false, err
	}
	if ia.Size() != ib.Size() {
		return false, nil
	}
	const chunk = 64 << 10
	ba, bb := make([]byte, chunk), make([]byte, chunk)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil
		}
		if ea != nil || eb != nil {
			if isEOF(ea) && isEOF(eb) {
				return true, nil
			}
			if !isEOF(ea) {
				return false, ea
			}
			return false, eb
		}
	}
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// Resolver applies a decision to conflicts under Root. Displaced files are
// moved to Archive/<timestamp>/<relative path>.
type Resolver struct {
	Root    string
	Archive string
	// Now is the clock; the default is time.Now.
	Now func() time.Time
}

func (r *Resolver) abs(rel string) (string, error) {
	rel = path.Clean(filepath.ToSlash(rel))
	if rel == "." || path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%q is not a path inside the job's folder", rel)
	}
	return filepath.Join(r.Root, filepath.FromSlash(rel)), nil
}

// archive moves the file at rel out of the way and returns where it went.
func (r *Resolver) archive(stamp, rel string) (string, error) {
	src, err := r.abs(rel)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(r.Archive, stamp, filepath.FromSlash(rel))
	if err := move(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func (r *Resolver) stamp() string {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	return now().Format("20060102-150405")
}

// KeepOriginal keeps the file under its real name and archives every conflict
// copy.
func (r *Resolver) KeepOriginal(c Conflict) ([]string, error) {
	if !c.Exists {
		return nil, fmt.Errorf("%s no longer exists under its own name; keep a conflict copy instead", c.Original.Path)
	}
	stamp := r.stamp()
	var done []string
	for _, v := range c.Variants {
		to, err := r.archive(stamp, v.Path)
		if err != nil {
			return done, err
		}
		done = append(done, fmt.Sprintf("archived %s -> %s", v.Path, to))
	}
	return done, nil
}

// KeepVariant puts conflict copy n under the real name. The file it replaces
// and the other copies are archived.
func (r *Resolver) KeepVariant(c Conflict, n int) ([]string, error) {
	var keep *Variant
	for i := range c.Variants {
		if c.Variants[i].N == n {
			keep = &c.Variants[i]
		}
	}
	if keep == nil {
		return nil, fmt.Errorf("%s has no conflict%d", c.Original.Path, n)
	}
	stamp := r.stamp()
	var done []string
	if c.Exists {
		to, err := r.archive(stamp, c.Original.Path)
		if err != nil {
			return done, err
		}
		done = append(done, fmt.Sprintf("archived %s -> %s", c.Original.Path, to))
	}
	from, err := r.abs(keep.Path)
	if err != nil {
		return done, err
	}
	to, err := r.abs(c.Original.Path)
	if err != nil {
		return done, err
	}
	if err := move(from, to); err != nil {
		return done, err
	}
	done = append(done, fmt.Sprintf("%s is now %s", keep.Path, c.Original.Path))
	for _, v := range c.Variants {
		if v.N == n {
			continue
		}
		dst, err := r.archive(stamp, v.Path)
		if err != nil {
			return done, err
		}
		done = append(done, fmt.Sprintf("archived %s -> %s", v.Path, dst))
	}
	return done, nil
}

// KeepBoth gives every conflict copy a name of its own, `name (conflict N).ext`,
// which bisync no longer treats as a conflict. Nothing is archived.
func (r *Resolver) KeepBoth(c Conflict) ([]string, error) {
	var done []string
	ext := path.Ext(c.Original.Path)
	stem := strings.TrimSuffix(c.Original.Path, ext)
	for _, v := range c.Variants {
		newRel := fmt.Sprintf("%s (conflict %d)%s", stem, v.N, ext)
		from, err := r.abs(v.Path)
		if err != nil {
			return done, err
		}
		to, err := r.abs(newRel)
		if err != nil {
			return done, err
		}
		if _, err := os.Lstat(to); err == nil {
			return done, fmt.Errorf("%s already exists", newRel)
		}
		if err := move(from, to); err != nil {
			return done, err
		}
		done = append(done, fmt.Sprintf("renamed %s -> %s", v.Path, newRel))
	}
	return done, nil
}

// Clean archives every conflict copy whose content is identical to the
// original, which loses nothing. It returns the actions taken.
func (r *Resolver) Clean(conflicts []Conflict) ([]string, error) {
	stamp := r.stamp()
	var done []string
	for _, c := range conflicts {
		if !c.Exists {
			continue
		}
		orig, err := r.abs(c.Original.Path)
		if err != nil {
			return done, err
		}
		for _, v := range c.Variants {
			p, err := r.abs(v.Path)
			if err != nil {
				return done, err
			}
			same, err := SameContent(orig, p)
			if err != nil {
				return done, err
			}
			if !same {
				continue
			}
			to, err := r.archive(stamp, v.Path)
			if err != nil {
				return done, err
			}
			done = append(done, fmt.Sprintf("archived %s (identical to %s) -> %s", v.Path, c.Original.Path, to))
		}
	}
	return done, nil
}

// move renames src to dst, creating dst's folder. It falls back to copy and
// remove when the two are on different filesystems.
func move(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil && !samePath(src, dst) {
		// Only reachable for the archive, where a file already sits at the
		// target: keep both rather than overwrite.
		dst = fmt.Sprintf("%s.%d", dst, time.Now().UnixNano())
	}
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var le *os.LinkError
	if !errors.As(err, &le) {
		return err
	}
	in, oerr := os.Open(src)
	if oerr != nil {
		return err
	}
	defer in.Close()
	info, serr := in.Stat()
	if serr != nil {
		return err
	}
	out, cerr := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if cerr != nil {
		return cerr
	}
	if _, cpErr := io.Copy(out, in); cpErr != nil {
		out.Close()
		os.Remove(dst)
		return cpErr
	}
	if clErr := out.Close(); clErr != nil {
		os.Remove(dst)
		return clErr
	}
	os.Chtimes(dst, info.ModTime(), info.ModTime())
	return os.Remove(src)
}

func samePath(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }
