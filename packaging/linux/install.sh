#!/bin/sh
# Install foxcloud and foxcloudd for the current user and enable the daemon.
# Usage: install.sh [dir containing the built binaries]  (default: build from source into ./bin)
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
src=${1:-}
# With no directory given, build from the source tree so a re-run never
# reinstalls an old build left in ./bin; a given directory is used as it is.
[ -n "$src" ] || { src=$root/bin; rm -f "$src/foxcloud" "$src/foxcloudd" "$src/foxcloud-tray"; }
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
	# The Lucide icons of the change feed: from the release archive's copy, else
	# from the submodule (git submodule update --init).
	lucide=$here/plasmoid/lucide
	[ -d "$lucide" ] || lucide=$root/third_party/lucide/icons
	if [ -d "$lucide" ]; then
		mkdir -p "$stage/tf.went.foxcloud/contents/icons/lucide"
		while read -r name; do
			[ -n "$name" ] && cp "$lucide/$name.svg" "$stage/tf.went.foxcloud/contents/icons/lucide/"
		done < "$here/plasmoid/lucide-icons.txt"
		for lic in "$lucide/LICENSE" "$lucide/../LICENSE"; do
			[ -f "$lic" ] && cp "$lic" "$stage/tf.went.foxcloud/contents/icons/lucide/LICENSE" && break
		done
	else
		echo "note: Lucide icons not found (run: git submodule update --init); the change feed will show no icons"
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
