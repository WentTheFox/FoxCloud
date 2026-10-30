// Command foxcloud runs and inspects FoxCloud sync jobs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/engine"
	"github.com/WentTheFox/FoxCloud/internal/secret"
	"github.com/WentTheFox/FoxCloud/internal/state"
)

const usage = `usage: foxcloud [-config path] <command> [args]

commands:
  run [-dry-run] [-force] <job>   run one sync pass
  status [job]                    show last success, failures and pauses
  resume <job>                    clear a pause after fixing the cause
  secret generate                 create the crypt master key in the OS keyring
  secret show [name]              print a secret, to back it up offline
  secret set <name>               store a secret read from stdin (restore a backup)
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

	if flag.Arg(0) == "secret" {
		return secretCmd(flag.Args()[1:])
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

func secretCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("secret needs a subcommand: generate, show or set")
	}
	switch args[0] {
	case "generate":
		if err := secret.GenerateAll(); err != nil {
			return err
		}
		fmt.Println("Created", strings.Join(secret.Names, " and "), "in the OS keyring.")
		fmt.Println("Back them up offline now: run `foxcloud secret show` in your own terminal.")
		fmt.Println("Losing them means losing every encrypted file.")
		return nil
	case "show":
		names := secret.Names
		if len(args) > 1 {
			names = args[1:2]
		}
		for _, n := range names {
			v, err := secret.Get(n)
			if err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
			fmt.Printf("%s: %s\n", n, v)
		}
		return nil
	case "set":
		if len(args) != 2 || !slices.Contains(secret.Names, args[1]) {
			return fmt.Errorf("set needs one of: %s", strings.Join(secret.Names, ", "))
		}
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		v := strings.TrimRight(string(b), "\r\n")
		if v == "" {
			return errors.New("empty secret on stdin")
		}
		return secret.Set(args[1], v)
	default:
		return fmt.Errorf("unknown secret subcommand %q", args[0])
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
