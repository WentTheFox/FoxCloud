package tray

import (
	"context"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const personalizeKey = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`

// DetectTheme reads whether the taskbar is light or dark.
func DetectTheme() Theme {
	k, err := registry.OpenKey(registry.CURRENT_USER, personalizeKey, registry.QUERY_VALUE)
	if err != nil {
		return ThemeDark
	}
	defer k.Close()
	return themeFrom(k)
}

func themeFrom(k registry.Key) Theme {
	if v, _, err := k.GetIntegerValue("SystemUsesLightTheme"); err == nil && v == 1 {
		return ThemeLight
	}
	return ThemeDark
}

var procRegNotifyChangeKeyValue = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegNotifyChangeKeyValue")

const regNotifyChangeLastSet = 0x4

// WatchTheme calls onChange whenever the taskbar theme flips, until ctx is
// cancelled. It blocks on a registry change notification, so nothing polls.
func WatchTheme(ctx context.Context, onChange func(Theme)) error {
	return watchKey(ctx, personalizeKey, onChange)
}

func watchKey(ctx context.Context, path string, onChange func(Theme)) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.NOTIFY|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	changed, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(changed)
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(stop)
	go func() {
		<-ctx.Done()
		windows.SetEvent(stop)
	}()

	last := themeFrom(k)
	for {
		// A registration fires once; it has to be renewed after every change.
		r, _, _ := procRegNotifyChangeKeyValue.Call(uintptr(k), 0, regNotifyChangeLastSet, uintptr(changed), 1)
		if r != 0 {
			return syscall.Errno(r)
		}
		ev, err := windows.WaitForMultipleObjects([]windows.Handle{changed, stop}, false, windows.INFINITE)
		if err != nil {
			return err
		}
		if ev == windows.WAIT_OBJECT_0+1 {
			return nil
		}
		if t := themeFrom(k); t != last {
			last = t
			onChange(t)
		}
	}
}
