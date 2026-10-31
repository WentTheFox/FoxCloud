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

- **Mount role (built; run on Linux with real FUSE against a local sshd, not on Windows).** The daemon runs
  `rclone mount --vfs-cache-mode <mode> --vfs-cache-max-size <size>` in the
  foreground for each `mount` job, reports `mounted` (the synced icon), and
  retries a minute after a failure; missing WinFsp/FUSE and a busy mount point
  are classified in plain English. Readiness is the one deliberate poll: a
  250 ms check, bounded to 30 s, that the mount point has appeared, because
  rclone gives a foreground mount no ready signal that works on every OS
  (replace it if one turns up). On Linux (fuse3, rclone 1.75) it mounts, reads through the crypt layer and
  unmounts when the daemon stops: `rclone.Run` now asks rclone to stop with
  SIGINT (15 s grace, then kill) instead of killing it, since a kill left a
  stale mount behind. WinFsp is still untested. Open: `foxcloud pin <path>` to keep chosen folders in the cache
  (a pull job with a filter file does this today), and
  the required-mount check for jobs that sit on the mount.

- **TODO: smarter handling of mixed-case name collisions.** Seen with the old
  Proton Drive setup: files whose names differ only by letter case (`Notes.txt`
  and `notes.txt`) are fine on a case-sensitive side (Linux, the crypt remote)
  but cannot exist side by side on a case-insensitive one (Windows, default
  macOS), so a sync between platforms stalls and can be left in a state that
  needs hand repair. Today the classifier only pauses with "rename one, then
  resume" (`case-clash`). Ideas to work through:
  - A pre-flight scan before a pass (and `foxcloud check-case <job>`, like
    `check-names`): list case-only collisions on the local side, and on the
    remote with a `lsf` listing, before rclone trips over them. Needs the
    platform's case rules, since the clash only matters where the local
    filesystem folds case.
  - An explicit conflict policy per job instead of a hard stop: keep the newest
    under its name and rename the other to `name (case conflict <short id>).ext`
    on the side that allows it, never delete either, log exactly what moved.
    Renaming on the remote is a real rename, so bisync sees it as one change
    rather than a delete plus a create.
  - A recoverable pause: after the fix, `resume` should continue from the last
    good baseline, not force a full `--resync` (which the bisync filter-change
    path does today); only fall back to a resync when rclone insists.
  - `push` is safe by construction (the source is the truth); the risk is
    `bisync` and `pull` on Windows/macOS, so start there.
  - Check how rclone's bisync `--case-insensitive` / `--ignore-case-sync`
    options and its own handling behave first; they may cover part of this.

- **Change journal (built in a first version; checked live against the Storage
  Box, not yet run between two real machines).** Machines tell each other what
  they uploaded, so one learns of another's change in about a second without
  listing the whole remote (about 4,500 folders over SFTP). Opt-in with
  `[journal] enabled = true`; `foxcloud journal-check` tests it end to end
  (measured here: a record written over SSH came back through `tail -F` in
  2.5 s on Windows, including connecting).
  - **Transport:** one shared log on the server, outside the crypt root
    (`foxcloud-journal/journal.log`), used through the SSH shell the Storage Box
    provides: `dd oflag=append` to write (each write under 8 KB, so two machines
    never interleave lines), `stat` to size, and a long-lived `tail -c +N -F` to
    follow, with keepalives and automatic reconnect that resumes from the saved
    byte offset, so nothing written while offline is lost. No polling; the
    30 s stamp-file stat from the first sketch turned out to be unnecessary.
    A 280 s idle connection still delivered.
  - **Privacy:** every line is AES-256-GCM encrypted with a key derived (HKDF)
    from the crypt master key, so the server sees neither names nor activity.
  - **The journal is a hint, never the truth.** Gaps are detected (a machine's
    sequence numbers jump, its generation id changes after it lost state, a
    line does not decrypt, the log is shorter than the saved offset) and each
    one brings a full pass forward on every job that mirrors the remote. Records
    are never acted on without the fetch itself checking the remote, and nothing
    happens before a job's start-up pass is done (the start-up hold is never cut
    short).
  - **Writing:** after a push or bisync job uploads file by file, the applied
    changes are recorded by a single ordered writer (paths as they appear in the
    crypt root; for bisync only uploads and folders, since only those were
    applied). Changes made by full passes are not journaled yet, so other
    machines learn of those from their own periodic pass.
  - **Reading (bisync and pull jobs):** a plainly new file is downloaded by
    name (`--ignore-existing`, no listing); a changed file that also exists
    locally, and every delete or move, brings a full bisync pass forward,
    because only bisync can tell a conflict from an update. A pull job
    downloads with `--update` and a backup folder and ignores deletes (it never
    deletes). Folders are created. Excluded paths are skipped by the job's filter.
  - **Not yet:** log rotation or compaction (the file grows ~100 bytes per
    change; a reset is handled as a gap); laptops using the `mount` role
    (rclone's directory cache could be refreshed for just the named folders
    through its remote control, which needs `--rc` on the mount); journaling
    full-pass changes; two real machines running it together; phones (they only
    download for now).

- **Incremental sync and shutdown guard (built; the Windows block reason and
  the real rclone calls are tested, a real shutdown and the Linux inhibitor are
  not).** With `watch = true` on a `push` or `bisync` job, the daemon runs the
  start-up pass, then watches the folder (fsnotify, one watch per folder; on
  Linux mind `fs.inotify.max_user_watches`). Each burst of changes, once the
  folder has been quiet for 3 s (30 s at most under constant activity), is
  turned into operations by an in-memory picture of the tree (`internal/delta`)
  and sent to the remote file by file:
  - new and changed files are uploaded by name (`--files-from-raw`, so no
    remote listing); rclone cannot combine that with a filter, so the job's
    filter file is applied by FoxCloud itself (`internal/filter`, checked
    against real rclone on a sample tree);
  - a renamed file or folder is detected (same size and time, unique
    look-alike) and done as one server-side `moveto` instead of a delete plus
    a re-upload; an ambiguous pair is left as delete and upload;
  - for `push`, deletions apply too (`purge` for folders), unless they exceed
    `max_delete`, in which case a full pass decides;
  - for `bisync`, only uploads happen, with `--update` so a file another machine
    made newer is never overwritten; deletions and moves wait for the next full
    pass, because only bisync can tell a delete from a conflict;
  - lost events (watcher overflow), a failed upload or a pause hand over to a
    normal full pass, which reports the cause properly.
  The full pass (`interval`) is now a rare safety net for remote-side changes:
  set it to hours, or leave it out and use `foxcloud sync-now`. Gaps: a file
  created between the first pass and the watcher start waits for the next full
  pass; a full pass's downloads under bisync make no-op upload attempts; the
  incremental path does not update the recorded last-success time.
  Pending uploads keep the machine from shutting down: while changes are
  noticed but not yet uploaded, or a push/bisync pass runs, the daemon holds a
  logind inhibitor lock on Linux (`shutdown:sleep`, blocks) and a shutdown
  block reason on a hidden window on Windows (the "FoxCloud is still uploading
  changes" screen; the user can still force it). A pull pass never holds it.

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

- **Updates (done: notify, manual, auto, rollback; none run against a real
  release).** `[update] mode` is `notify` (default), `auto` or `off`. The daemon
  checks GitHub once after start-up and then about daily with jitter (timers,
  no polling). `foxcloud update [-check|-rollback]` and the tray's update item
  download the platform archive, verify it against the release `checksums.txt`
  (and, on Windows, the pinned self-signed certificate on every executable),
  refuse while a job is syncing, swap `foxcloud`, `foxcloudd` and
  `foxcloud-tray` keeping `*.old`, and say to restart. In `auto` mode the
  daemon does the same once the release is `auto_delay` old (24 h by default,
  so a bad release can be pulled first): it downloads and checks while sync
  carries on, then waits for every running pass to finish and holds new ones
  off (`Daemon.Exclusive`) just for the file swap, logs it and exits with
  status 75. The service manager restarts it: systemd's `Restart=on-failure`
  does; the Windows task's restart settings should (a non-zero exit is a
  failure) but that is untested. On Linux a throwaway user unit with
  `Restart=on-failure` restarted a process that exited 75, as expected. A failed check or install changes nothing and
  is tried again at the next check. The trust chain is weak (no detached
  release signature; see CI builds), which is why `auto` is opt-in. Open:
  - Mounts are interrupted by the restart (rclone is stopped with the daemon).
  - Nothing restarts the tray, which keeps running the old binary until
    restarted.
  - A crash loop after a bad auto-update is not detected; rollback is manual
    (`foxcloud update -rollback`).
  - Where a package manager is used (winget, Homebrew, an AUR package), let it
    update and skip the built-in updater.

- **Tray (built; on Plasma 6 it registers with the StatusNotifier and shows
  the offline state, the live states are untested).** On Plasma the widget is
  the status icon and `install.sh` skips the tray's autostart. `foxcloud-tray` (Windows and
  Linux; macOS needs cgo and is deferred) shows one summary icon (failed >
  paused > activity > waiting/idle > synced), a line per job (click to sync
  now), the "service not running" state with the amber icon, and the update
  notice the daemon publishes. It reconnects without polling by watching the
  socket's directory (`daemon.Follow`). Icons are generated from the SVGs by
  `tools/genicons` (its own module) into `internal/tray/icons`. The theme
  follows the desktop live, event-driven: a registry change notification on
  Windows, the portal's `SettingChanged` D-Bus signal on Linux (`-theme dark|light`
  pins it; without a portal it stays dark).
  Autostart: a Run-key entry on Windows, an XDG autostart file on Linux. Update
  item: click it to run `foxcloud update` (checksum, signature, refuses mid-sync),
  with "Installing…", "Installed vX: restart the service" and "Update failed:
  <reason> (click to retry)" shown in place; `foxcloud update` now also swaps
  `foxcloud-tray` when the release has it. Open: restarting the daemon (and the
  tray) after an install, which is the auto-update mode below.

- **Plasma 6 widget (loads in Plasma 6 and sits in the system tray; the live
  job cards are untested).** It is a system-tray applet: enable it under
  System Tray settings > Entries. A stopped service shows a steady amber
  icon; only a failed or paused sync pulses.
  `packaging/linux/plasmoid/tf.went.foxcloud`, ported from the old repo's
  plasmoid and installed by `install.sh` with `kpackagetool6`. QML cannot open
  a unix socket, so it uses the old widget's event-driven trick: it runs
  `foxcloud status-json -wait REV` through the executable data engine; that
  command blocks inside foxcloud (on the daemon's event stream, or on the
  socket file appearing) until the state differs from REV, prints the new JSON
  and exits, and the widget re-arms it with the new revision. No timer; the
  only one is a 30 s retry when the `foxcloud` command itself cannot run. Shows
  the same summary icon as the tray, a card per job with Sync now / Stop, the
  offline state and the update notice. It was written without a Plasma
  session to test in, so expect small QML fixes on first load.

- **Daemon as a service.** Linux is done: `packaging/linux/` has a systemd user
  unit (waits for boot via `systemctl is-system-running --wait`, logs to the
  journal) and `install.sh`. Windows is done as a logon scheduled task, not a
  service (`packaging/windows/install.ps1`): the crypt key sits in the user's
  credential store, which a service account cannot read. The daemon takes
  `-logfile` (rotated at 5 MiB). The Linux unit is installed and verified (`systemd-analyze verify`; the
  boot hold returns `running` on a live system); the daemon has run under
  neither wrapper against the real box yet.
  Linux packaging now has the unit, the tray autostart and the widget.
