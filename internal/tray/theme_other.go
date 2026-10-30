//go:build !windows && !linux

package tray

import "context"

// DetectTheme assumes a dark panel; there is no portable way to ask here.
func DetectTheme() Theme { return ThemeDark }

// WatchTheme has nothing to listen to on this platform.
func WatchTheme(ctx context.Context, onChange func(Theme)) error {
	<-ctx.Done()
	return nil
}
