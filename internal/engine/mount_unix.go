//go:build !windows

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// checkMounted fails unless path is a mount point, so a missing drive can't
// make a sync fill the root disk.
func checkMounted(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	parent, err := os.Stat(filepath.Dir(filepath.Clean(path)))
	if err != nil {
		return err
	}
	a, aok := fi.Sys().(*syscall.Stat_t)
	b, bok := parent.Sys().(*syscall.Stat_t)
	if !aok || !bok {
		return fmt.Errorf("cannot inspect %s", path)
	}
	if a.Dev == b.Dev {
		return fmt.Errorf("%s is not a mount point", path)
	}
	return nil
}
