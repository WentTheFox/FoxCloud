//go:build !windows

package tray

// DetectTheme assumes a dark panel, which is what most Linux panels are;
// there is no portable way to ask. Use the tray's -theme flag to override.
func DetectTheme() Theme { return ThemeDark }
