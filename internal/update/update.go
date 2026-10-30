// Package update finds, verifies and installs new FoxCloud releases.
//
// The chain of trust is deliberately plain: the release comes from this
// project's GitHub repository over HTTPS, the archive must match the
// checksums.txt published with it, and on Windows the executables must also
// carry the project's (self-signed) Authenticode certificate. There is no
// detached release signature, so a compromised GitHub account could ship a
// bad release; nothing here installs one without a person asking.
package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is where releases are published.
const Repo = "WentTheFox/FoxCloud"

// apiBase is overridden by tests.
var apiBase = "https://api.github.com"

// Release is the newest published release.
type Release struct {
	Tag    string
	Assets map[string]string // asset name -> download URL
}

// Archive names the archive built for this platform, e.g.
// foxcloud-v1.2.3-windows-amd64.zip.
func (r Release) Archive() string {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("foxcloud-%s-%s-%s%s", r.Tag, runtime.GOOS, runtime.GOARCH, ext)
}

var client = &http.Client{Timeout: 5 * time.Minute}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "foxcloud-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// Latest asks GitHub for the newest release.
func Latest(ctx context.Context) (Release, error) {
	resp, err := get(ctx, apiBase+"/repos/"+Repo+"/releases/latest")
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	var raw struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		return Release{}, err
	}
	r := Release{Tag: raw.Tag, Assets: map[string]string{}}
	for _, a := range raw.Assets {
		r.Assets[a.Name] = a.URL
	}
	return r, nil
}

// Newer reports whether tag is a later release than current. A "dev" build
// (or anything that is not vMAJOR.MINOR.PATCH) is never offered an update.
func Newer(current, tag string) bool {
	c, ok1 := parse(current)
	t, ok2 := parse(tag)
	if !ok1 || !ok2 {
		return false
	}
	for i := range c {
		if t[i] != c[i] {
			return t[i] > c[i]
		}
	}
	return false
}

// Releasable reports whether v is a released version, so worth checking
// against newer ones. A local "dev" build is not.
func Releasable(v string) bool {
	_, ok := parse(v)
	return ok
}

func parse(v string) (out [3]int, ok bool) {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Download fetches the platform archive and checks it against the release's
// checksums.txt. It returns the archive's path inside dir.
func Download(ctx context.Context, r Release, dir string) (string, error) {
	name := r.Archive()
	archiveURL, ok := r.Assets[name]
	if !ok {
		return "", fmt.Errorf("release %s has no build for %s/%s (%s)", r.Tag, runtime.GOOS, runtime.GOARCH, name)
	}
	sumsURL, ok := r.Assets["checksums.txt"]
	if !ok {
		return "", fmt.Errorf("release %s has no checksums.txt; refusing to install it", r.Tag)
	}

	resp, err := get(ctx, sumsURL)
	if err != nil {
		return "", err
	}
	sums, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return "", err
	}
	want, err := checksumFor(string(sums), name)
	if err != nil {
		return "", err
	}

	resp, err = get(ctx, archiveURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	dst := filepath.Join(dir, name)
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 512<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(dst)
		return "", fmt.Errorf("checksum mismatch for %s: expected %s, got %s", name, want, got)
	}
	return dst, nil
}

func checksumFor(sums, name string) (string, error) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

// Binaries are the executables an update replaces, without extension.
var Binaries = []string{"foxcloud", "foxcloudd"}

func exeName(b string) string {
	if runtime.GOOS == "windows" {
		return b + ".exe"
	}
	return b
}

// Extract unpacks the two executables from the archive into dir and returns
// their paths by name. Only those exact files are read from the archive, and
// only their base names are used, so a crafted path cannot escape dir.
func Extract(archive, dir string) (map[string]string, error) {
	want := map[string]bool{}
	for _, b := range Binaries {
		want[exeName(b)] = true
	}
	out := map[string]string{}
	save := func(base string, r io.Reader) error {
		dst := filepath.Join(dir, base)
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, io.LimitReader(r, 512<<20))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		out[strings.TrimSuffix(base, ".exe")] = dst
		return err
	}

	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		for _, zf := range zr.File {
			base := path.Base(zf.Name)
			if zf.FileInfo().IsDir() || !want[base] {
				continue
			}
			rc, err := zf.Open()
			if err != nil {
				return nil, err
			}
			err = save(base, rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
		}
	} else {
		f, err := os.Open(archive)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, err
			}
			base := path.Base(h.Name)
			if h.Typeflag != tar.TypeReg || !want[base] {
				continue
			}
			if err := save(base, tr); err != nil {
				return nil, err
			}
		}
	}
	for _, b := range Binaries {
		if out[b] == "" {
			return nil, fmt.Errorf("the archive has no %s", exeName(b))
		}
	}
	return out, nil
}

// Install replaces the executables in dir with the new ones. Each old file is
// kept as <name>.old (Windows lets a running executable be renamed but not
// overwritten), and everything is put back if a step fails.
func Install(dir string, files map[string]string) error {
	type swap struct{ cur, old string }
	var done []swap
	rollback := func() {
		for i := len(done) - 1; i >= 0; i-- {
			os.Remove(done[i].cur)
			os.Rename(done[i].old, done[i].cur)
		}
	}
	for _, b := range Binaries {
		cur := filepath.Join(dir, exeName(b))
		old := cur + ".old"
		staged := cur + ".new"
		if err := copyFile(files[b], staged); err != nil {
			rollback()
			return err
		}
		os.Remove(old)
		if err := os.Rename(cur, old); err != nil && !errors.Is(err, os.ErrNotExist) {
			os.Remove(staged)
			rollback()
			return err
		}
		done = append(done, swap{cur, old})
		if err := os.Rename(staged, cur); err != nil {
			rollback()
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
