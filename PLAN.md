# FoxCloud implementation plan

A cross-platform, self-hosted sync solution built on [rclone](https://rclone.org).
Successor to RcloneProtonDrive, no longer tied to any one provider. Files are
encrypted client-side, so the storage server only ever holds ciphertext.

## Target architecture

- One authoritative desktop pushes to a cheap SFTP host (Hetzner Storage Box).
- Laptops mount the remote with files on demand (small SSD, large dataset).
- Phones use existing rclone-based apps (Round Sync, RCX, FolderSync) against
  the same crypt remote.

Workload this is sized for: ~20k files, ~4.5k folders, ~390 GB. Metadata
scanning is not the bottleneck; laptop on-demand access, mobile experience and
monthly cost are.

## Remote shape

```
local folder  <->  crypt: (client-side)  <->  sftp: (Storage Box, port 23)
```

- **crypt**: rclone crypt (XSalsa20-Poly1305), file and directory names
  encrypted. Names are limited to about 143 characters.
- **sftp**: SSH key auth only, host key pinned. Any SFTP server works.
- No plaintext hashes through crypt, so comparison is size + modtime (SFTP
  preserves modtimes).
- **Key handling**: crypt password and salt are generated, kept out of
  plaintext config (`--password-command` / `RCLONE_CONFIG_PASS`, backed by the OS
  keychain) and must be backed up offline before the first upload. Losing them
  means losing the data.
- **Snapshots**: enable Storage Box snapshots as the safety net against a
  corrupted desktop syncing its damage upward.

## Roles

A machine runs one or more jobs, each with a role:

| Role     | rclone                                   | Use                                          |
|----------|------------------------------------------|----------------------------------------------|
| `push`   | `sync local -> remote`                   | Authoritative desktop                        |
| `pull`   | `copy remote -> local`                   | Never deletes locally, backs up overwrites   |
| `bisync` | `bisync`                                 | Multi-writer machines                        |
| `mount`  | `mount --vfs-cache-mode full`            | Laptop on demand (WinFsp / FUSE required)    |

Safety nets carried over from RcloneProtonDrive: timestamped backup dir,
`--max-delete`, required-mount check, pause state needing a human to resume,
automatic first-run `--resync`, dry-run before enabling.

## Design

One core, thin per-OS shells (the reference repo's duplicated PowerShell and
bash logic diverged).

- **Core (`foxcloudd`, Go)**: loads config, runs jobs through rclone's rc API,
  classifies errors, persists state (last success, run history, pause flag).
  Single static binary per OS.
- **CLI (`foxcloud`)**: `status`, `sync-now`, `stop`, `pause`, `resume`,
  `doctor`, `init`, `export-config`.
- **IPC**: unix socket / named pipe. Frontends subscribe to status events.
- **Triggers** (no polling): schedule timer, debounced filesystem watcher for
  `push`, IPC `sync-now`.
- **Config**: TOML, see `config.example.toml`.

### Known problems from the reference repo to fix from day one

- Surface the real cause of a failure (permission denied, case-only duplicate
  filenames, auth, network) in plain English, not an opaque HTTP 500.
- Progress estimates from run history, not rclone's cumulative check counter.
- Persist "last successful sync" across restarts.
- Tray state driven by job-finished events so it can't stay stuck on "syncing".

## Milestones

1. **Skeleton** (this commit): layout, config schema, docs.
2. **Core engine (CLI only)**: `push`/`pull`/`bisync` jobs, safety nets, error
   classifier, persisted state. Tested against rclone's `local` backend.
3. **Daemon + IPC**: scheduler, fs watcher, `foxcloud status|sync-now|pause|resume`.
4. **Linux packaging**: systemd user units, install script, Plasma 6 widget
   (ported from the reference), optional generic SNI tray.
5. **Windows packaging**: service, tray, installer. Lucide icons via a
   `third_party/lucide` submodule (added here).
6. **Mount role**: Windows (WinFsp) and Linux, cache size limits for a 256 GB SSD.
7. **Setup guides**: Storage Box + crypt end to end, `foxcloud doctor`
   (crypt round-trip canary, pinned host key, key backup confirmed, snapshots),
   mobile guide.

macOS is deferred; the Go core keeps it open.

## Planned layout

```
cmd/foxcloud/       CLI
cmd/foxcloudd/      daemon
internal/           config, jobs, rclone rc client, errors, state, ipc
packaging/linux/    systemd units, install.sh, plasmoid
packaging/windows/  service, tray, installer
docs/               setup guides
```

## Decisions

- Go for the core.
- Hetzner Storage Box over SFTP is the first target; the design stays
  server-agnostic.
- Filenames and directory names are encrypted (143-character limit, no
  server-side browsing accepted).

## Backlog

- **File watching (done for `push`).** `watch = true` syncs 30 s after changes
  stop, using OS file notifications (fsnotify), one watch per folder. On Linux
  the inotify limit (`fs.inotify.max_user_watches`) may be too low for a very
  large tree; folders that cannot be watched are logged and the interval still
  covers them. Changes to files the filter excludes also trigger a pass. Not
  for `pull`/`bisync`, which would trigger themselves.

- **Let the system settle after boot before the first sync.** The daemon and
  the tray must not start a pass the instant the machine comes up: boot-time
  disk activity (indexing, updates, other services starting) competes with an
  immediate sync, and a sync that starts at once on a drive that is still
  spinning up or being scanned makes both slower. Needed:
  - A startup hold before the first run, with a minimum delay (done:
    `[daemon] startup_delay`, shown as the `waiting` state) and a "system still
    settling" condition (the unit and service wrappers below).
  - A visible state for it. The tray and widget should show that FoxCloud is
    waiting on purpose, not stuck. For now it reuses the idle icon, with the
    tooltip and `foxcloud status` saying "waiting for the system to settle"
    and when the hold ends. A dedicated icon can come later if the two states
    prove hard to tell apart.
  - Event-driven where possible, not polling. Linux: have the unit wait on
    `systemctl is-system-running --wait` (blocks until boot finishes) plus a
    timer delay. Windows: a delayed-start service and a fixed minimum wait.
    Detecting "disk is quiet" generally needs periodic sampling, so if that is
    added it has to be called out as the one place polling is unavoidable and
    kept to a short, bounded window.
  - `sync-now` and a manual resume skip the hold.

- **CI builds.** Written (`.github/workflows/ci.yml` tests on all three OSes;
  `release.yml` builds on a `v*` tag): `foxcloud` and `foxcloudd` for
  Windows amd64/arm64, Linux amd64/arm64 and macOS arm64, archives plus a
  `checksums.txt` as a GitHub release. Decision: Windows binaries are
  self-signed in CI with a certificate whose key is in the repo secrets
  `WINDOWS_SIGN_PFX_BASE64` and `WINDOWS_SIGN_PFX_PASSWORD` (offline copy in
  `D:\Backups\foxcloud\signing`, public certificate in
  `packaging/windows/`). Nothing else is signed, no macOS notarisation, so
  SmartScreen and Gatekeeper will still warn. First release `v0.1.0` built and signed on GitHub. Later:
  the packaging milestones plug into the same release job.

- **Updates (notify and manual done, unattended install not).** The daemon
  checks GitHub for a newer release once after start-up and then about daily
  with jitter (timers, no polling), and logs "update available"; `[update]
  check = false` turns the request off. `foxcloud update [-check]` downloads the
  platform archive, verifies it against the release `checksums.txt` (and, on
  Windows, that both executables carry the pinned self-signed certificate),
  refuses while a job is syncing, swaps the binaries keeping `*.old`, and
  tells you to restart the daemon. The trust chain is weak (no detached release
  signature; see CI builds), which is why nothing installs on its own. Not yet
  run against a real release. Open:
  - An opt-in `mode = "auto"` that installs when every job is idle and then
    restarts the daemon (the supervisors need a restart-on-exit policy first).
  - "Update available" shows in the tray (done); "restarting to update" and the
    widget are open.
  - A `foxcloud update --rollback` that restores the `*.old` files.
  - Where a package manager is used (winget, Homebrew, an AUR package), let it
    update and skip the built-in updater.

- **Tray (built, untested on a real desktop).** `foxcloud-tray` (Windows and
  Linux; macOS needs cgo and is deferred) shows one summary icon (failed >
  paused > activity > waiting/idle > synced), a line per job (click to sync
  now), the "service not running" state with the amber icon, and the update
  notice the daemon publishes. It reconnects without polling by watching the
  socket's directory (`daemon.Follow`). Icons are generated from the SVGs by
  `tools/genicons` (its own module) into `internal/tray/icons`. Theme is read
  once at start on Windows and assumed dark on Linux (`-theme` overrides).
  Autostart: a Run-key entry on Windows, an XDG autostart file on Linux. Open:
  live theme changes, a "restarting to update" state, tray in the release
  archives' updater (`foxcloud update` still swaps only `foxcloud` and
  `foxcloudd`), and the Plasma widget.

- **Daemon as a service.** Linux is done: `packaging/linux/` has a systemd user
  unit (waits for boot via `systemctl is-system-running --wait`, logs to the
  journal) and `install.sh`. Windows is done as a logon scheduled task, not a
  service (`packaging/windows/install.ps1`): the crypt key sits in the user's
  credential store, which a service account cannot read. The daemon takes
  `-logfile` (rotated at 5 MiB). Neither wrapper has been run for real yet.
  Still to do: the Plasma widget from milestone 4.
