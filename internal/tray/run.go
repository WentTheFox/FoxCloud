//go:build !darwin

package tray

import (
	"context"
	"fmt"
	"sync"

	"fyne.io/systray"

	"github.com/WentTheFox/FoxCloud/internal/daemon"
)

// Run shows the tray icon and follows the daemon at sock until the user
// quits or ctx is cancelled. It blocks, and must be called from the main
// goroutine on some platforms.
func Run(ctx context.Context, sock string, theme Theme, logf func(string, ...any)) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		model    = NewModel()
		jobItems = map[string]*systray.MenuItem{}
		offline  *systray.MenuItem
		updItem  *systray.MenuItem
	)

	render := func() {
		systray.SetIcon(IconBytes(theme, model.Icon()))
		systray.SetTooltip(model.Tooltip())
		systray.SetTitle("FoxCloud")

		if model.Offline {
			offline.Show()
		} else {
			offline.Hide()
		}
		live := map[string]bool{}
		for _, ev := range model.Jobs() {
			live[ev.Job] = true
			it := jobItems[ev.Job]
			if it == nil {
				it = systray.AddMenuItem("", "Click to sync now")
				jobItems[ev.Job] = it
				job := ev.Job
				go func() {
					for {
						select {
						case <-it.ClickedCh:
							go func() {
								sock, _ := daemon.SocketPath()
								if _, err := daemon.Do(sock, daemon.Request{Cmd: "sync-now", Job: job}); err != nil {
									logf("sync-now %s: %v", job, err)
								}
							}()
						case <-ctx.Done():
							return
						}
					}
				}()
			}
			it.SetTitle(JobLine(ev))
			it.Show()
		}
		for name, it := range jobItems {
			if !live[name] {
				it.Hide() // the daemon is gone or no longer lists it
			}
		}
		if tag := model.Update(); tag != "" {
			updItem.SetTitle(UpdateLine(tag))
			updItem.Show()
		} else {
			updItem.Hide()
		}
	}

	systray.Run(func() {
		offline = systray.AddMenuItem("FoxCloud service is not running", "Start the foxcloudd service")
		offline.Disable()
		updItem = systray.AddMenuItem("", "A newer release is available")
		updItem.Disable()
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit tray", "Close this icon; sync keeps running")

		mu.Lock()
		model.SetOffline() // until the daemon says otherwise
		render()
		mu.Unlock()

		go func() {
			select {
			case <-quit.ClickedCh:
				systray.Quit()
			case <-ctx.Done():
				systray.Quit()
			}
		}()
		go func() {
			err := daemon.Follow(ctx, sock, func(ev daemon.Event) {
				mu.Lock()
				defer mu.Unlock()
				model.Apply(ev)
				render()
			}, func() {
				mu.Lock()
				defer mu.Unlock()
				model.SetOffline()
				render()
			})
			if err != nil {
				logf("tray: %v", err)
				fmt.Println("tray: lost the daemon and cannot wait for it:", err)
			}
		}()
	}, cancel)
}
