//go:build !darwin

package tray

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"fyne.io/systray"

	"github.com/WentTheFox/FoxCloud/internal/daemon"
)

// Run shows the tray icon and follows the daemon at sock until the user
// quits or ctx is cancelled. It blocks, and must be called from the main
// goroutine on some platforms. With followTheme the icons switch palette when
// the desktop's theme changes; otherwise theme stays as given.
func Run(ctx context.Context, sock string, theme Theme, followTheme bool, logf func(string, ...any)) {
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
		if text := model.UpdateText(); text != "" {
			updItem.SetTitle(text)
			if model.Installing() {
				updItem.Disable()
			} else {
				updItem.Enable()
			}
			updItem.Show()
		} else {
			updItem.Hide()
		}
	}

	systray.Run(func() {
		offline = systray.AddMenuItem("FoxCloud service is not running", "Start the foxcloudd service")
		offline.Disable()
		updItem = systray.AddMenuItem("", "Download, verify and install the newer release")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit tray", "Close this icon; sync keeps running")

		mu.Lock()
		model.SetOffline() // until the daemon says otherwise
		render()
		mu.Unlock()

		go func() {
			for {
				select {
				case <-updItem.ClickedCh:
					mu.Lock()
					if model.Installing() || model.Update() == "" {
						mu.Unlock()
						continue
					}
					tag := model.Update()
					model.SetInstall(InstallRunning, "")
					render()
					mu.Unlock()

					// foxcloud update does the work: it checks the checksum and the
					// signature, and refuses while a job is syncing.
					out, err := runUpdate()
					mu.Lock()
					if err != nil {
						model.SetInstall(InstallFailed, lastLine(out, err))
						logf("update: %v: %s", err, out)
					} else {
						model.SetInstall(InstallDone, tag)
					}
					render()
					mu.Unlock()
				case <-ctx.Done():
					return
				}
			}
		}()
		go func() {
			select {
			case <-quit.ClickedCh:
				systray.Quit()
			case <-ctx.Done():
				systray.Quit()
			}
		}()
		if followTheme {
			go func() {
				err := WatchTheme(ctx, func(t Theme) {
					mu.Lock()
					defer mu.Unlock()
					theme = t
					render()
				})
				if err != nil {
					logf("not following theme changes: %v", err)
				}
			}()
		}
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

// runUpdate runs `foxcloud update`, from beside this executable when it is
// there (the installed layout), else from the PATH.
func runUpdate() (string, error) {
	bin := "foxcloud"
	if exe, err := os.Executable(); err == nil {
		name := "foxcloud"
		if filepath.Ext(exe) == ".exe" {
			name += ".exe"
		}
		if p := filepath.Join(filepath.Dir(exe), name); fileExists(p) {
			bin = p
		}
	}
	out, err := exec.Command(bin, "update").CombinedOutput()
	return string(out), err
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// lastLine is the last non-empty line of a command's output, which is where
// foxcloud puts the reason; it falls back to the error itself.
func lastLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return err.Error()
}
