//go:build !darwin

package tray

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/WentTheFox/FoxCloud/internal/daemon"
	"github.com/WentTheFox/FoxCloud/internal/proc"
)

// maxJobSlots is how many jobs the menu can list.
const maxJobSlots = 12

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
		slots    []*systray.MenuItem
		slotJob  [maxJobSlots]string
		offline  *systray.MenuItem
		syncAll  *systray.MenuItem
		updItem  *systray.MenuItem
	)

	render := func() {
		systray.SetIcon(IconBytes(theme, model.Icon()))
		systray.SetTooltip(model.Tooltip())
		systray.SetTitle("FoxCloud")

		if model.Offline {
			offline.Show()
			syncAll.Hide()
		} else {
			offline.Hide()
			if len(model.Jobs()) > 0 {
				syncAll.Show()
			} else {
				syncAll.Hide()
			}
		}
		// Jobs fill the pre-made slots in name order. The slots sit above the
		// separator; an item added later would land below Quit.
		jobs := model.Jobs()
		for i, it := range slots {
			if i < len(jobs) {
				slotJob[i] = jobs[i].Job
				it.SetTitle(JobLine(jobs[i]))
				it.Show()
			} else {
				slotJob[i] = ""
				it.Hide()
			}
		}
		if len(jobs) > len(slots) {
			logf("the menu has room for %d jobs; %d are not listed", len(slots), len(jobs)-len(slots))
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
		systray.SetOnTapped(togglePopup(logf))
		offline = systray.AddMenuItem("Start FoxCloud service", "FoxCloud is not running: click to start the daemon")
		syncAll = systray.AddMenuItem("Sync all now", "Sync every job now")
		syncAll.Hide()
		go func() {
			for {
				select {
				case <-offline.ClickedCh:
					if out, err := startService(); err != nil {
						logf("start service: %v: %s", err, out)
					}
				case <-syncAll.ClickedCh:
					mu.Lock()
					jobs := model.Jobs()
					mu.Unlock()
					sock, _ := daemon.SocketPath()
					for _, ev := range jobs {
						if _, err := daemon.Do(sock, daemon.Request{Cmd: "sync-now", Job: ev.Job}); err != nil {
							logf("sync-now %s: %v", ev.Job, err)
						}
					}
				case <-ctx.Done():
					return
				}
			}
		}()
		for i := 0; i < maxJobSlots; i++ {
			it := systray.AddMenuItem("", "Click to sync now")
			it.Hide()
			slots = append(slots, it)
			go func() {
				for {
					select {
					case <-it.ClickedCh:
						mu.Lock()
						job := slotJob[i]
						mu.Unlock()
						if job == "" {
							continue
						}
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
			err := daemon.Follow(ctx, sock, func() {
				// Connected: a daemon with no jobs sends no events, and must
				// still stop showing as offline.
				mu.Lock()
				defer mu.Unlock()
				model.SetOnline()
				render()
			}, func(ev daemon.Event) {
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

// startService starts the daemon the way the installer set it up: the
// scheduled task on Windows, the user service elsewhere.
func startService() (string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("schtasks", "/run", "/tn", "FoxCloud daemon")
	} else {
		cmd = exec.Command("systemctl", "--user", "start", "foxcloudd")
	}
	out, err := proc.Hide(cmd).CombinedOutput()
	return string(out), err
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
	out, err := proc.Hide(exec.Command(bin, "update")).CombinedOutput()
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

// togglePopup returns the handler for a left click: open the status popup, or
// close it if it is already open. The popup closes itself when it loses focus,
// which happens as the click lands on the tray, so a click that arrives just
// after it went away is the one that closed it and must not reopen it.
func togglePopup(logf func(string, ...any)) func() {
	var (
		mu       sync.Mutex
		cur      *exec.Cmd
		closedAt time.Time
	)
	return func() {
		script := popupScript()
		if script == "" {
			return // no popup installed (Linux uses the Plasma widget)
		}
		mu.Lock()
		defer mu.Unlock()
		if cur != nil {
			_ = cur.Process.Kill()
			return
		}
		if time.Since(closedAt) < 400*time.Millisecond {
			return
		}
		cmd := proc.Hide(exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
			"-STA", "-File", script))
		if err := cmd.Start(); err != nil {
			logf("popup: %v", err)
			return
		}
		cur = cmd
		go func() {
			_ = cmd.Wait()
			mu.Lock()
			cur, closedAt = nil, time.Now()
			mu.Unlock()
		}()
	}
}

// popupScript is foxcloud-popup.ps1 beside this executable, or "".
func popupScript() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if p := filepath.Join(filepath.Dir(exe), "foxcloud-popup.ps1"); fileExists(p) {
		return p
	}
	return ""
}
