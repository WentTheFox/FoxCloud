package tray

import "golang.org/x/sys/windows/registry"

// DetectTheme reads whether the taskbar is light or dark. It is read once at
// start-up; a change while running needs a restart of the tray.
func DetectTheme() Theme {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return ThemeDark
	}
	defer k.Close()
	if v, _, err := k.GetIntegerValue("SystemUsesLightTheme"); err == nil && v == 1 {
		return ThemeLight
	}
	return ThemeDark
}
