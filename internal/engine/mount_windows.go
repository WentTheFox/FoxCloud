//go:build windows

package engine

import (
	"fmt"
	"os"
)

// checkMounted fails unless path exists as a directory, e.g. a drive root that
// is only present while the drive is attached.
func checkMounted(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}
