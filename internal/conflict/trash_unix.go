//go:build !windows

package conflict

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// MoveToTrash puts a file in the system trash, where it can be restored.
func MoveToTrash(p string) error {
	if runtime.GOOS == "darwin" {
		return trashMac(p)
	}
	// gio knows the trash rules of every mounted drive; the others are the
	// common command line tools of the KDE and freedesktop worlds.
	for _, c := range [][]string{
		{"gio", "trash", "--", p},
		{"trash-put", "--", p},
		{"kioclient6", "move", p, "trash:/"},
		{"kioclient5", "move", p, "trash:/"},
	} {
		bin, err := exec.LookPath(c[0])
		if err != nil {
			continue
		}
		if out, err := exec.Command(bin, c[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w: %s", c[0], err, trimOutput(out))
		}
		return nil
	}
	return errors.New("no trash tool found (install glib's gio, trash-cli or KDE's kioclient)")
}

func trashMac(p string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dst := filepath.Join(home, ".Trash", filepath.Base(p))
	if _, err := os.Lstat(dst); err == nil {
		dst += time.Now().Format(" 15.04.05")
	}
	return os.Rename(p, dst)
}

func trimOutput(b []byte) string {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return string(b)
}
