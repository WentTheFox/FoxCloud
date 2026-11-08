#!/bin/sh
# Turns secrets passed as environment variables into the files FoxCloud reads.
# Nothing secret is baked into the image; the files live in /tmp (not a volume).
#   FOXCLOUD_CRYPT_KEY    the master key (config: key_command = "printenv FOXCLOUD_CRYPT_KEY")
#   FOXCLOUD_SSH_KEY      the private key, PEM text (config: key_file = "/tmp/foxcloud/ssh_key")
#   FOXCLOUD_KNOWN_HOSTS  known_hosts lines (config: known_hosts_file = "/tmp/foxcloud/known_hosts")
set -eu
umask 077
d=/tmp/foxcloud
mkdir -p "$d"
if [ -n "${FOXCLOUD_SSH_KEY:-}" ]; then
  printf '%s\n' "$FOXCLOUD_SSH_KEY" > "$d/ssh_key"
fi
if [ -n "${FOXCLOUD_KNOWN_HOSTS:-}" ]; then
  printf '%s\n' "$FOXCLOUD_KNOWN_HOSTS" > "$d/known_hosts"
fi

# The shadow mount lives in a SUBDIRECTORY of the shared host folder, never on
# the bind mount itself. If this container is killed or crashes, its FUSE mount
# is left behind on the host as "Transport endpoint is not connected"; when the
# dead mount IS the bind source, `docker start` then fails outright ("invalid
# mount config for type bind: stat ...: transport endpoint is not connected").
# With a subdirectory the container can always start, and this clears the
# leftover (the unmount propagates to the host via the rshared bind).
# Keep in sync with mount_point in config.toml.
m="${FOXCLOUD_MOUNT_POINT:-/mnt/foxcloud/fox}"
fusermount3 -uz "$m" 2>/dev/null || true
mkdir -p "$m"

exec /usr/local/bin/foxcloudd "$@"
