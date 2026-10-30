package tray

import "testing"

func TestSchemeTheme(t *testing.T) {
	for v, want := range map[any]Theme{
		uint32(0): ThemeDark, // no preference
		uint32(1): ThemeDark,
		uint32(2): ThemeLight,
		"junk":    ThemeDark,
	} {
		if got := schemeTheme(v); got != want {
			t.Errorf("schemeTheme(%v) = %s, want %s", v, got, want)
		}
	}
}
