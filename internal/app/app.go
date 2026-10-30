// Package app holds the start-up steps the foxcloud CLI and the foxcloudd
// daemon share: find the config, open the state store, locate rclone.
package app

import (
	"errors"
	"io"
	"os/exec"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/engine"
	"github.com/WentTheFox/FoxCloud/internal/state"
)

// Load reads the config (from path, or the default location when empty) and
// returns it with an engine ready to run jobs. out receives rclone's output.
func Load(path string, out io.Writer) (*config.Config, *engine.Engine, error) {
	if path == "" {
		var err error
		if path, err = config.DefaultPath(); err != nil {
			return nil, nil, err
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	dir, err := state.Dir()
	if err != nil {
		return nil, nil, err
	}
	bin, err := exec.LookPath("rclone")
	if err != nil {
		return nil, nil, errors.New("rclone not found in PATH")
	}
	return cfg, &engine.Engine{Cfg: cfg, Store: state.Store{Dir: dir}, Rclone: bin, Out: out}, nil
}
