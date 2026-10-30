// Command foxcloudd runs FoxCloud's sync jobs in the background and reports
// their state to clients such as the tray and the foxcloud CLI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/app"
	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/daemon"
	"github.com/WentTheFox/FoxCloud/internal/engine"
	"github.com/WentTheFox/FoxCloud/internal/rclone"
	"github.com/WentTheFox/FoxCloud/internal/update"
	"github.com/WentTheFox/FoxCloud/internal/version"
)

// restartExit is the exit status after installing an update. It is non-zero on
// purpose: the service managers (systemd's Restart=on-failure, the Windows
// task's restart settings) only restart a process that "failed".
const restartExit = 75

var errRestart = errors.New("restarting to run the update")

func main() {
	if err := run(); errors.Is(err, errRestart) {
		os.Exit(restartExit)
	} else if err != nil {
		fmt.Fprintln(os.Stderr, "foxcloudd:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "config file (default: $FOXCLOUD_CONFIG, else user config dir)")
	logFile := flag.String("logfile", "", "append the log to this file instead of stderr (rotated at 5 MiB)")
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Version)
		return nil
	}

	logw := io.Writer(os.Stderr)
	if *logFile != "" {
		f, err := openLog(*logFile)
		if err != nil {
			return err
		}
		defer f.Close()
		logw = f
	}

	cfg, e, err := app.Load(*configPath, logw)
	if err != nil {
		return err
	}
	sock, err := daemon.SocketPath()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	d := daemon.New(cfg.Jobs, func(ctx context.Context, job string, onResync func(), onDirection func(rclone.Direction)) error {
		return e.Run(ctx, job, engine.RunOptions{OnResync: onResync, OnDirection: onDirection})
	}, cfg.Daemon.StartupDelayDuration())

	d.Mount = func(ctx context.Context, job string, onReady func()) error {
		return e.Mount(ctx, job, onReady)
	}
	d.Logf = func(format string, args ...any) {
		fmt.Fprintf(logw, "foxcloudd: "+format+"\n", args...)
	}

	var restart atomic.Bool
	if cfg.Update.CheckEnabled() && update.Releasable(version.Version) {
		go update.Announce(runCtx, version.Version, cfg.Daemon.StartupDelayDuration()+time.Minute,
			func(r update.Release) {
				d.SetUpdate(r.Tag)
				if cfg.Update.EffectiveMode() != config.UpdateAuto {
					d.Logf("update available: %s (this is %s); run `foxcloud update` to install it", r.Tag, version.Version)
					return
				}
				if !update.OldEnough(r, cfg.Update.AutoDelayDuration(), time.Now()) {
					d.Logf("update %s is newer than %s but not yet %s old; auto mode will take it at a later check",
						r.Tag, version.Version, cfg.Update.AutoDelayDuration())
					return
				}
				if err := autoInstall(runCtx, d, r); err != nil {
					d.Logf("auto-update to %s failed, nothing was changed: %v", r.Tag, err)
					return
				}
				d.Logf("installed %s over %s (previous files kept as *.old); restarting", r.Tag, version.Version)
				restart.Store(true)
				cancel()
			}, d.Logf)
	}

	// If the socket cannot be served (another daemon owns it, say), stop
	// everything: a daemon nobody can talk to is worse than none.
	srvErr := make(chan error, 1)
	go func() {
		srvErr <- daemon.Serve(runCtx, d, sock)
		cancel()
	}()

	fmt.Fprintf(logw, "foxcloudd: listening on %s; first pass held for %s\n",
		sock, cfg.Daemon.StartupDelayDuration())
	d.Run(runCtx)
	err = <-srvErr
	if restart.Load() {
		return errRestart
	}
	return err
}

// autoInstall downloads and checks the release while sync carries on, then
// takes the moment when no pass is running to swap the files.
func autoInstall(ctx context.Context, d *daemon.Daemon, r update.Release) error {
	dir, err := update.ExecDir()
	if err != nil {
		return err
	}
	staged, err := update.Prepare(ctx, r)
	if err != nil {
		return err
	}
	defer staged.Close()
	return d.Exclusive(func() error { return staged.Install(dir) })
}

// maxLogSize is when the log file is moved aside to <file>.1 (one generation).
const maxLogSize = 5 << 20

// openLog opens the log for appending, first rotating it if it has grown large.
func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogSize {
		os.Remove(path + ".1")
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, err
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}
