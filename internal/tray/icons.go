package tray

import (
	"embed"
	"runtime"
)

// Generated from assets/status by tools/genicons; run it again after changing
// an SVG.
//
//go:embed icons
var iconFS embed.FS

// Theme picks the icon palette: "dark" has pale glyphs for a dark panel,
// "light" has deep ones for a light panel.
type Theme string

const (
	ThemeDark  Theme = "dark"
	ThemeLight Theme = "light"
)

// IconBytes returns the image for an icon in the given theme, in the format the
// platform's tray wants: ICO on Windows, PNG elsewhere.
func IconBytes(theme Theme, icon Icon) []byte {
	ext := ".png"
	if runtime.GOOS == "windows" {
		ext = ".ico"
	}
	b, err := iconFS.ReadFile("icons/" + string(theme) + "-" + string(icon) + ext)
	if err != nil {
		return nil
	}
	return b
}
