#!/bin/sh
# Install foxcloud and foxcloudd for the current user and enable the daemon.
# Usage: install.sh [dir containing the built binaries]  (default: ./bin, else build)
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
src=${1:-$root/bin}
bindir=$HOME/.local/bin
unitdir=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user

if [ ! -x "$src/foxcloudd" ] || [ ! -x "$src/foxcloud" ]; then
	echo "building into $src"
	mkdir -p "$src"
	(cd "$root" && CGO_ENABLED=0 go build -o "$src/" ./cmd/foxcloud ./cmd/foxcloudd ./cmd/foxcloud-tray)
fi

# On Plasma the widget is the status icon, so the separate tray icon would be a
# duplicate: install it (and its autostart) only elsewhere.
plasma=
case ":${XDG_CURRENT_DESKTOP:-}:" in *:KDE:*)
	command -v kpackagetool6 >/dev/null 2>&1 && [ -d "$here/plasmoid/tf.went.foxcloud" ] && plasma=1 ;;
esac

mkdir -p "$bindir" "$unitdir"
install -m 0755 "$src/foxcloud" "$src/foxcloudd" "$bindir/"
if [ -x "$src/foxcloud-tray" ]; then
	install -m 0755 "$src/foxcloud-tray" "$bindir/"
fi
if [ -x "$src/foxcloud-tray" ] && [ -z "$plasma" ]; then
	autostart=${XDG_CONFIG_HOME:-$HOME/.config}/autostart
	mkdir -p "$autostart"
	sed "s|^Exec=.*|Exec=$bindir/foxcloud-tray|" "$here/foxcloud-tray.desktop" > "$autostart/foxcloud-tray.desktop"
fi
install -m 0644 "$here/foxcloudd.service" "$unitdir/"

# The Plasma widget, if Plasma 6's package tool is here. The icons are staged
# next to it: from the release archive's copy, else from the source tree.
if command -v kpackagetool6 >/dev/null 2>&1 && [ -d "$here/plasmoid/tf.went.foxcloud" ]; then
	stage=$(mktemp -d)
	trap 'rm -rf "$stage"' EXIT
	cp -r "$here/plasmoid/tf.went.foxcloud" "$stage/"
	mkdir -p "$stage/tf.went.foxcloud/contents/icons"
	if [ -d "$here/plasmoid/icons" ]; then
		cp "$here"/plasmoid/icons/*.png "$stage/tf.went.foxcloud/contents/icons/"
	else
		cp "$root"/internal/tray/icons/*.png "$stage/tf.went.foxcloud/contents/icons/"
	fi
	kpackagetool6 -t Plasma/Applet -u "$stage/tf.went.foxcloud" 2>/dev/null ||
		kpackagetool6 -t Plasma/Applet -i "$stage/tf.went.foxcloud"
	echo "widget installed: enable \"FoxCloud\" under System Tray settings > Entries (restart plasmashell if it is not listed)"
fi

systemctl --user daemon-reload
systemctl --user enable foxcloudd.service
echo "installed. Start it with: systemctl --user start foxcloudd"
echo "Logs: journalctl --user -u foxcloudd -f"
echo "Make sure the config is found: set FOXCLOUD_CONFIG in ${XDG_CONFIG_HOME:-$HOME/.config}/foxcloud/env"
