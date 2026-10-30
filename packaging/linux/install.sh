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
	(cd "$root" && CGO_ENABLED=0 go build -o "$src/" ./cmd/foxcloud ./cmd/foxcloudd)
fi

mkdir -p "$bindir" "$unitdir"
install -m 0755 "$src/foxcloud" "$src/foxcloudd" "$bindir/"
install -m 0644 "$here/foxcloudd.service" "$unitdir/"

systemctl --user daemon-reload
systemctl --user enable foxcloudd.service
echo "installed. Start it with: systemctl --user start foxcloudd"
echo "Logs: journalctl --user -u foxcloudd -f"
echo "Make sure the config is found: set FOXCLOUD_CONFIG in ${XDG_CONFIG_HOME:-$HOME/.config}/foxcloud/env"
