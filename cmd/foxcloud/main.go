// Command foxcloud runs and inspects FoxCloud sync jobs.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/app"
	"github.com/WentTheFox/FoxCloud/internal/daemon"
	"github.com/WentTheFox/FoxCloud/internal/engine"
	"github.com/WentTheFox/FoxCloud/internal/journal"
	"github.com/WentTheFox/FoxCloud/internal/secret"
	"github.com/WentTheFox/FoxCloud/internal/update"
	"github.com/WentTheFox/FoxCloud/internal/version"
)

const usage = `usage: foxcloud [-config path] <command> [args]

commands:
  run [-dry-run] [-force] <job>   run one sync pass
  status [job]                    show last success, failures and pauses
  usage                           show how full the remote storage is
  check-names <job>               list names too long to upload once encrypted
  verify [-n 25] [-max-size 200M] <job>
                                  download a random sample, decrypt it and compare with the originals
  resume <job>                    clear a pause after fixing the cause
  sync-now <job>                  ask the running daemon to sync a job now
  stop <job>                      ask the daemon to cancel a job's running pass
  update [-check|-rollback]       install the newest release (-check only looks, -rollback undoes the last one)
  status-json [-wait REV]         the daemon's state as JSON; with -wait, block until it differs from REV
  journal-check                   append a test record to the shared journal and time it coming back
  version                         print the build version
  watch                           follow what the daemon's jobs are doing
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

	if flag.Arg(0) == "status-json" {
		return statusJSONCmd(flag.Args()[1:])
	}
	if flag.Arg(0) == "update" {
		return updateCmd(flag.Args()[1:])
	}
	if flag.Arg(0) == "version" {
		fmt.Println(version.Version)
		return nil
	}
	if flag.Arg(0) == "secret" {
		return secretCmd(flag.Args()[1:])
	}

	switch flag.Arg(0) {
	case "sync-now", "stop", "watch":
		return daemonCmd(flag.Arg(0), flag.Args()[1:])
	}

	_, e, err := app.Load(*configPath, os.Stderr)
	if err != nil {
		return err
	}

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
	case "usage":
		return showUsage(ctx, e)
	case "journal-check":
		return journalCheck(ctx, e)
	case "verify":
		fs := flag.NewFlagSet("verify", flag.ExitOnError)
		n := fs.Int("n", 25, "how many random files to download and compare")
		maxSize := fs.String("max-size", "200M", "skip files larger than this when sampling (empty for no limit)")
		fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("verify needs exactly one job name")
		}
		res, err := e.Verify(ctx, fs.Arg(0), *n, *maxSize)
		if err != nil {
			return err
		}
		fmt.Printf("Verified %d of %d eligible files: each was downloaded, decrypted and matched its original byte for byte.\n", res.Sampled, res.Candidates)
		return nil
	case "check-names":
		if len(args) != 1 {
			return errors.New("check-names needs exactly one job name")
		}
		bad, err := e.CheckNames(ctx, args[0])
		if err != nil {
			return err
		}
		for _, p := range bad {
			fmt.Println(p)
		}
		if len(bad) > 0 {
			return fmt.Errorf("%d path(s) have a name too long to upload once encrypted", len(bad))
		}
		fmt.Println("All names fit.")
		return nil
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
		var b []byte
		var err error
		if fi, serr := os.Stdin.Stat(); serr == nil && fi.Mode()&os.ModeCharDevice != 0 {
			// Typed at a terminal: say what is expected and stop at the line end.
			fmt.Fprintf(os.Stderr, "Paste the value for %s, then press Enter: ", args[1])
			b, err = bufio.NewReader(os.Stdin).ReadBytes('\n')
			if errors.Is(err, io.EOF) {
				err = nil
			}
		} else {
			b, err = io.ReadAll(os.Stdin)
		}
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

// daemonCmd handles the commands that talk to a running foxcloudd.
func daemonCmd(cmd string, args []string) error {
	sock, err := daemon.SocketPath()
	if err != nil {
		return err
	}
	notRunning := func(err error) error {
		if errors.Is(err, daemon.ErrNotRunning) {
			return fmt.Errorf("%w; use `foxcloud run <job>` for a single pass", err)
		}
		return err
	}
	if cmd == "watch" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return notRunning(daemon.Watch(ctx, sock, func(ev daemon.Event) {
			if ev.Job == "" {
				fmt.Printf("%s  update available: %s (run `foxcloud update`)\n", ev.At.Local().Format(time.TimeOnly), ev.Update)
				return
			}
			line := fmt.Sprintf("%s  %-12s %s", ev.At.Local().Format(time.TimeOnly), ev.Job, ev.State)
			if ev.Message != "" {
				line += "  " + ev.Message
			}
			if ev.Until != nil {
				line += "  (until " + ev.Until.Local().Format(time.TimeOnly) + ")"
			}
			fmt.Println(line)
		}))
	}
	if len(args) != 1 {
		return fmt.Errorf("%s needs exactly one job name", cmd)
	}
	if _, err := daemon.Do(sock, daemon.Request{Cmd: cmd, Job: args[0]}); err != nil {
		return notRunning(err)
	}
	fmt.Printf("%s: asked the daemon (%s)\n", args[0], cmd)
	return nil
}

// warnPercent is the fill level at which usage starts warning.
const warnPercent = 90

func showUsage(ctx context.Context, e *engine.Engine) error {
	u, err := e.Usage(ctx)
	if err != nil {
		return err
	}
	pct := u.UsedPercent()
	fmt.Printf("Storage: %s of %s used (%.1f%%), %s free\n",
		formatBytes(u.Used), formatBytes(u.Total), pct, formatBytes(u.Free))
	if pct >= warnPercent {
		fmt.Printf("WARNING: the storage is %.0f%% full; uploads will start failing when it fills up.\n", pct)
	}
	return nil
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format(time.DateTime)
}

// updateCmd installs the newest release over the running executables. It will
// not do so while the daemon is mid-sync: the daemon has to be restarted
// afterwards, and that would cut the pass short.
func updateCmd(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	checkOnly := fs.Bool("check", false, "only report whether a newer release exists")
	rollback := fs.Bool("rollback", false, "put back the version saved by the last update")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *rollback {
		dir, err := update.ExecDir()
		if err != nil {
			return err
		}
		restored, err := update.Rollback(dir)
		if err != nil {
			return err
		}
		fmt.Printf("restored %s in %s; restart the daemon to run it\n", strings.Join(restored, ", "), dir)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if !update.Releasable(version.Version) {
		return fmt.Errorf("this is a %q build, not a release, so there is nothing to compare with", version.Version)
	}
	r, err := update.Latest(ctx)
	if err != nil {
		return err
	}
	if !update.Newer(version.Version, r.Tag) {
		fmt.Printf("up to date (%s)\n", version.Version)
		return nil
	}
	fmt.Printf("%s is available (this is %s)\n", r.Tag, version.Version)
	if *checkOnly {
		return nil
	}

	if sock, err := daemon.SocketPath(); err == nil {
		if resp, err := daemon.Do(sock, daemon.Request{Cmd: "status"}); err == nil {
			for _, ev := range resp.Events {
				switch ev.State {
				case daemon.StateUploading, daemon.StateDownloading, daemon.StateSyncing:
					return fmt.Errorf("job %q is syncing; update once it has finished", ev.Job)
				}
			}
		}
	}

	dir, err := update.ExecDir()
	if err != nil {
		return err
	}
	if err := update.Apply(ctx, r, dir); err != nil {
		return err
	}
	fmt.Printf("installed %s in %s; the previous files are kept as *.old\n", r.Tag, dir)
	fmt.Println("restart the daemon to run it (Linux: systemctl --user restart foxcloudd; Windows: Stop-ScheduledTask / Start-ScheduledTask \"FoxCloud daemon\")")
	return nil
}

// statusJSONCmd prints the daemon's state as one JSON document. The Plasma
// widget runs it in a loop: each run with -wait blocks (no polling) until the
// state differs from the revision the widget last showed.
func statusJSONCmd(args []string) error {
	fs := flag.NewFlagSet("status-json", flag.ContinueOnError)
	wait := fs.String("wait", "", "block until the state differs from this revision")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sock, err := daemon.SocketPath()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var v daemon.View
	if *wait != "" {
		v, err = daemon.WaitView(ctx, sock, *wait)
	} else {
		v, err = daemon.CurrentView(sock)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(v)
}

// journalCheck proves the journal works end to end against the real server:
// it follows the log, appends one test record as a made-up machine, and times
// how long the record takes to come back. Other machines ignore the record.
func journalCheck(ctx context.Context, e *engine.Engine) error {
	jc, err := e.JournalClient(ctx)
	if err != nil {
		return err
	}
	ssh, ok := jc.Transport.(*journal.SSH)
	if !ok {
		return errors.New("the journal is not using SSH")
	}
	if err := ssh.Setup(ctx); err != nil {
		return err
	}
	size, err := ssh.Size(ctx)
	if err != nil {
		return fmt.Errorf("cannot reach the journal over SSH: %w", err)
	}
	fmt.Printf("journal %s on the server: %d bytes\n", e.Cfg.Journal.LogPath(), size)

	machine := fmt.Sprintf("check-%d", os.Getpid())
	line, err := jc.Codec.Encode(journal.Record{Machine: machine, Gen: "check", Seq: 1, Time: time.Now().Unix(), Op: "ping"})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	got := make(chan time.Time, 1)
	go ssh.Follow(ctx, size, func(l string, _ int64) {
		if r, err := jc.Codec.Decode(l); err == nil && r.Machine == machine {
			select {
			case got <- time.Now():
			default:
			}
		}
	})
	time.Sleep(2 * time.Second) // let tail -F connect before writing
	start := time.Now()
	if err := ssh.Append(ctx, line+"\n"); err != nil {
		return err
	}
	select {
	case at := <-got:
		fmt.Printf("ok: a record written over SSH came back through tail -F in %s\n", at.Sub(start).Round(time.Millisecond))
		return nil
	case <-ctx.Done():
		return errors.New("the record never came back through tail -F; the journal will not work on this server")
	}
}
