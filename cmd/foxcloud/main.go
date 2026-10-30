// Command foxcloud runs and inspects FoxCloud sync jobs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/engine"
	"github.com/WentTheFox/FoxCloud/internal/state"
)

const usage = `usage: foxcloud [-config path] <command> [args]

commands:
  run [-dry-run] [-force] <job>   run one sync pass
  status [job]                    show last success, failures and pauses
  resume <job>                    clear a pause after fixing the cause
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "foxcloud:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "config file (default: $FOXCLOUD_CONFIG, else user config dir)")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		return errors.New("no command given")
	}

	path := *configPath
	if path == "" {
		var err error
		if path, err = config.DefaultPath(); err != nil {
			return err
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	dir, err := state.Dir()
	if err != nil {
		return err
	}
	bin, err := exec.LookPath("rclone")
	if err != nil {
		return errors.New("rclone not found in PATH")
	}
	e := &engine.Engine{Cfg: cfg, Store: state.Store{Dir: dir}, Rclone: bin, Out: os.Stderr}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd, args := flag.Arg(0), flag.Args()[1:]
	switch cmd {
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		dry := fs.Bool("dry-run", false, "show what would change without changing anything")
		force := fs.Bool("force", false, "bypass the max-delete safety check")
		fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("run needs exactly one job name")
		}
		return e.Run(ctx, fs.Arg(0), engine.RunOptions{DryRun: *dry, Force: *force})
	case "status":
		return status(e, args)
	case "resume":
		if len(args) != 1 {
			return errors.New("resume needs exactly one job name")
		}
		return e.Resume(args[0])
	default:
		flag.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func status(e *engine.Engine, args []string) error {
	for _, j := range e.Cfg.Jobs {
		if len(args) > 0 && args[0] != j.Name {
			continue
		}
		st, err := e.Store.Load(j.Name)
		if err != nil {
			return err
		}
		fmt.Printf("%s (%s)\n", j.Name, j.Role)
		fmt.Printf("  last success: %s\n", formatTime(st.LastSuccess))
		if st.LastFailure != nil {
			fmt.Printf("  last failure: [%s] %s (%s)\n", st.LastFailure.Kind, st.LastFailure.Message, formatTime(st.LastFailure.At))
		}
		if st.Paused != nil {
			fmt.Printf("  PAUSED: %s\n", st.Paused.Message)
		}
	}
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format(time.DateTime)
}
