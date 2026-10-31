# FoxCloud

Self-hosted, cross-platform file sync on top of [rclone](https://rclone.org).
Files are encrypted on your machine (rclone crypt) and stored on any SFTP host,
such as a Hetzner Storage Box, so the server only sees ciphertext.

Status: planning. See [PLAN.md](PLAN.md) for the design and milestones and
[config.example.toml](config.example.toml) for the draft config.

Roles per machine: `push` (authoritative desktop), `pull`, `bisync`, and
`mount` (files on demand for laptops).

The Plasma widget uses icons from [Lucide](https://lucide.dev) (ISC licence, see
`third_party/lucide/LICENSE`); clone with `--recurse-submodules` or run
`git submodule update --init`.
