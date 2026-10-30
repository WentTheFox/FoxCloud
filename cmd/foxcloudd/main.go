// Command foxcloudd runs FoxCloud's sync jobs in the background and reports
// their state to clients such as the tray and the foxcloud CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/WentTheFox/FoxCloud/internal/app"
	"github.com/WentTheFox/FoxCloud/internal/daemon"
	"github.com/WentTheFox/FoxCloud/internal/engine"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "foxcloudd:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "config file (default: $FOXCLOUD_CONFIG, else user config dir)")
	flag.Parse()

	cfg, e, err := app.Load(*configPath, os.Stderr)
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

	d := daemon.New(cfg.Jobs, func(ctx context.Context, job string, onResync func()) error {
		return e.Run(ctx, job, engine.RunOptions{OnResync: onResync})
	}, cfg.Daemon.StartupDelayDuration())

	// If the socket cannot be served (another daemon owns it, say), stop
	// everything: a daemon nobody can talk to is worse than none.
	srvErr := make(chan error, 1)
	go func() {
		srvErr <- daemon.Serve(runCtx, d, sock)
		cancel()
	}()

	fmt.Fprintf(os.Stderr, "foxcloudd: listening on %s; first pass held for %s\n",
		sock, cfg.Daemon.StartupDelayDuration())
	d.Run(runCtx)
	return <-srvErr
}
