# FoxCloud

Self-hosted, cross-platform file sync on top of [rclone](https://rclone.org).
Files are encrypted on your machine (rclone crypt) and stored on any SFTP host,
such as a Hetzner Storage Box, so the server only sees ciphertext.

Status: planning. See [PLAN.md](PLAN.md) for the design and milestones and
[config.example.toml](config.example.toml) for the draft config.

Roles per machine: `push` (authoritative desktop), `pull`, `bisync`, and
`mount` (files on demand for laptops).
