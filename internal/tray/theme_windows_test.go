package tray

import (
	"context"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// Flips a throwaway key shaped like the real one and checks the watcher
// reports each flip once, without touching the user's actual theme.
func TestWatchKeyReportsThemeFlips(t *testing.T) {
	const path = `Software\FoxCloudTest\Personalize`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Skip("cannot create a test registry key:", err)
	}
	defer func() {
		k.Close()
		registry.DeleteKey(registry.CURRENT_USER, path)
		registry.DeleteKey(registry.CURRENT_USER, `Software\FoxCloudTest`)
	}()
	k.SetDWordValue("SystemUsesLightTheme", 0)

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Theme, 4)
	done := make(chan error, 1)
	go func() { done <- watchKey(ctx, path, func(th Theme) { got <- th }) }()
	time.Sleep(200 * time.Millisecond)

	expect := func(want Theme) {
		t.Helper()
		select {
		case th := <-got:
			if th != want {
				t.Fatalf("theme = %s, want %s", th, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no change reported (wanted %s)", want)
		}
	}
	k.SetDWordValue("SystemUsesLightTheme", 1)
	expect(ThemeLight)
	k.SetDWordValue("SystemUsesLightTheme", 1) // same value: not a change
	k.SetDWordValue("SystemUsesLightTheme", 0)
	expect(ThemeDark)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("watcher returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not stop when the context was cancelled")
	}
}
