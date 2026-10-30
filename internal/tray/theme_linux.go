package tray

import (
	"context"

	"github.com/godbus/dbus/v5"
)

const (
	portalDest   = "org.freedesktop.portal.Desktop"
	portalPath   = "/org/freedesktop/portal/desktop"
	portalIface  = "org.freedesktop.portal.Settings"
	appearanceNS = "org.freedesktop.appearance"
	colorScheme  = "color-scheme"
)

// schemeTheme maps the portal's color-scheme value: 1 is "prefer dark", 2 is
// "prefer light", 0 is "no preference". Without a preference, dark is assumed,
// as most panels are.
func schemeTheme(v any) Theme {
	if n, ok := v.(uint32); ok && n == 2 {
		return ThemeLight
	}
	return ThemeDark
}

// DetectTheme asks the desktop portal for the colour scheme, falling back to
// dark when there is no portal (the -theme flag overrides it).
func DetectTheme() Theme {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return ThemeDark
	}
	defer conn.Close()
	var v dbus.Variant
	call := conn.Object(portalDest, portalPath).Call(portalIface+".Read", 0, appearanceNS, colorScheme)
	if call.Err != nil || call.Store(&v) != nil {
		return ThemeDark
	}
	// Read wraps the value in a second variant.
	if inner, ok := v.Value().(dbus.Variant); ok {
		return schemeTheme(inner.Value())
	}
	return schemeTheme(v.Value())
}

// WatchTheme calls onChange when the desktop's colour scheme changes, until ctx
// is cancelled. It waits for the portal's SettingChanged signal over D-Bus, so
// nothing polls. It returns an error if there is no session bus or portal to
// listen to; the icon then just keeps the theme it started with.
func WatchTheme(ctx context.Context, onChange func(Theme)) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface(portalIface),
		dbus.WithMatchMember("SettingChanged"),
	); err != nil {
		return err
	}
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)

	last := DetectTheme()
	for {
		select {
		case <-ctx.Done():
			return nil
		case s, ok := <-signals:
			if !ok {
				return nil
			}
			if s.Name != portalIface+".SettingChanged" || len(s.Body) < 3 {
				continue
			}
			ns, _ := s.Body[0].(string)
			key, _ := s.Body[1].(string)
			val, _ := s.Body[2].(dbus.Variant)
			if ns != appearanceNS || key != colorScheme {
				continue
			}
			if t := schemeTheme(val.Value()); t != last {
				last = t
				onChange(t)
			}
		}
	}
}
