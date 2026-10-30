package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		cur, tag string
		want     bool
	}{
		{"v0.1.0", "v0.1.1", true},
		{"v0.1.9", "v0.2.0", true},
		{"v0.9.0", "v1.0.0", true},
		{"v0.1.1", "v0.1.1", false},
		{"v0.2.0", "v0.1.9", false},
		{"v0.10.0", "v0.9.0", false}, // numeric, not textual
		{"dev", "v9.9.9", false},
		{"v0.1.0", "nightly", false},
		{"v0.1.0-rc1", "v0.1.1", false},
	}
	for _, c := range cases {
		if got := Newer(c.cur, c.tag); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.cur, c.tag, got, c.want)
		}
	}
}

// archiveBytes builds a release archive for this platform holding the two
// executables inside a top-level folder, like the release workflow does.
func archiveBytes(t *testing.T, tag string, contents map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	dir := fmt.Sprintf("foxcloud-%s-%s-%s/", tag, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		for name, body := range contents {
			w, _ := zw.Create(dir + name)
			w.Write([]byte(body))
		}
		zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range contents {
		tw.WriteHeader(&tar.Header{Name: dir + name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func release(t *testing.T, tag string, archive []byte, sum string) (*httptest.Server, Release) {
	t.Helper()
	mux := http.NewServeMux()
	var r Release
	r.Tag = tag
	name := Release{Tag: tag}.Archive()
	mux.HandleFunc("/"+name, func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sum, name)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r.Assets = map[string]string{name: srv.URL + "/" + name, "checksums.txt": srv.URL + "/checksums.txt"}
	return srv, r
}

func sumOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestDownloadVerifiesChecksum(t *testing.T) {
	arc := archiveBytes(t, "v1.0.0", map[string]string{exeName("foxcloud"): "a", exeName("foxcloudd"): "b"})

	_, good := release(t, "v1.0.0", arc, sumOf(arc))
	if _, err := Download(context.Background(), good, t.TempDir()); err != nil {
		t.Fatalf("good archive rejected: %v", err)
	}

	dir := t.TempDir()
	_, bad := release(t, "v1.0.0", arc, sumOf([]byte("something else")))
	if _, err := Download(context.Background(), bad, dir); err == nil {
		t.Fatal("a checksum mismatch must be an error")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Error("the rejected download was left on disk")
	}
}

func TestDownloadRequiresChecksums(t *testing.T) {
	r := Release{Tag: "v1.0.0", Assets: map[string]string{Release{Tag: "v1.0.0"}.Archive(): "http://unused"}}
	if _, err := Download(context.Background(), r, t.TempDir()); err == nil {
		t.Error("a release without checksums.txt must be refused")
	}
}

func TestExtractAndInstallKeepOldBinaries(t *testing.T) {
	arc := archiveBytes(t, "v1.0.0", map[string]string{
		exeName("foxcloud"): "new-cli", exeName("foxcloudd"): "new-daemon", "README.md": "ignored",
	})
	archive := filepath.Join(t.TempDir(), Release{Tag: "v1.0.0"}.Archive())
	os.WriteFile(archive, arc, 0o644)

	files, err := Extract(archive, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, exeName("foxcloud")), []byte("old-cli"), 0o755)
	os.WriteFile(filepath.Join(dir, exeName("foxcloudd")), []byte("old-daemon"), 0o755)
	if err := Install(dir, files); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		exeName("foxcloud"): "new-cli", exeName("foxcloudd"): "new-daemon",
		exeName("foxcloud") + ".old": "old-cli", exeName("foxcloudd") + ".old": "old-daemon",
	} {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestExtractNeedsBothExecutables(t *testing.T) {
	arc := archiveBytes(t, "v1.0.0", map[string]string{exeName("foxcloud"): "only one"})
	archive := filepath.Join(t.TempDir(), Release{Tag: "v1.0.0"}.Archive())
	os.WriteFile(archive, arc, 0o644)
	if _, err := Extract(archive, t.TempDir()); err == nil {
		t.Error("an archive missing foxcloudd must be rejected")
	}
}

func TestLatestParsesRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+Repo+"/releases/latest" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"tag_name":"v1.2.3","assets":[{"name":"checksums.txt","browser_download_url":"http://x/c"}]}`)
	}))
	defer srv.Close()
	old := apiBase
	apiBase = srv.URL
	defer func() { apiBase = old }()

	r, err := Latest(context.Background())
	if err != nil || r.Tag != "v1.2.3" || r.Assets["checksums.txt"] != "http://x/c" {
		t.Errorf("Latest() = %+v, %v", r, err)
	}
}

func TestOptionalTrayIsInstalledWhenPresent(t *testing.T) {
	arc := archiveBytes(t, "v1.0.0", map[string]string{
		exeName("foxcloud"): "new-cli", exeName("foxcloudd"): "new-daemon", exeName("foxcloud-tray"): "new-tray",
	})
	archive := filepath.Join(t.TempDir(), Release{Tag: "v1.0.0"}.Archive())
	os.WriteFile(archive, arc, 0o644)
	files, err := Extract(archive, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, exeName("foxcloud-tray")), []byte("old-tray"), 0o755)
	if err := Install(dir, files); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, exeName("foxcloud-tray")))
	old, _ := os.ReadFile(filepath.Join(dir, exeName("foxcloud-tray")+".old"))
	if string(got) != "new-tray" || string(old) != "old-tray" {
		t.Errorf("tray = %q (old %q)", got, old)
	}
}

func TestMissingOptionalTrayIsFine(t *testing.T) {
	arc := archiveBytes(t, "v1.0.0", map[string]string{exeName("foxcloud"): "a", exeName("foxcloudd"): "b"})
	archive := filepath.Join(t.TempDir(), Release{Tag: "v1.0.0"}.Archive())
	os.WriteFile(archive, arc, 0o644)
	files, err := Extract(archive, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, exeName("foxcloud-tray")), []byte("keep"), 0o755)
	if err := Install(dir, files); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, exeName("foxcloud-tray"))); string(got) != "keep" {
		t.Errorf("a release without a tray must leave the installed one alone, got %q", got)
	}
}
