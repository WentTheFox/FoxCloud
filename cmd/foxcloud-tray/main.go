//go:build !darwin

// Command foxcloud-tray puts FoxCloud's status in the notification area: an
// icon for what the daemon is doing, a line per job (click to sync now), and
// a notice when a newer release exists. It only talks to foxcloudd; it does no
// syncing itself.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/WentTheFox/FoxCloud/internal/daemon"
	"github.com/WentTheFox/FoxCloud/internal/tray"
	"github.com/WentTheFox/FoxCloud/internal/version"
)

func main() {
	theme := flag.String("theme", "auto", "icon palette: auto, dark (for dark panels) or light")
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Version)
		return
	}

	t := tray.DetectTheme()
	switch *theme {
	case "dark":
		t = tray.ThemeDark
	case "light":
		t = tray.ThemeLight
	case "auto":
	default:
		fmt.Fprintf(os.Stderr, "foxcloud-tray: unknown theme %q\n", *theme)
		os.Exit(2)
	}

	sock, err := daemon.SocketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "foxcloud-tray:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	tray.Run(ctx, sock, t, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "foxcloud-tray: "+format+"\n", args...)
	})
}
