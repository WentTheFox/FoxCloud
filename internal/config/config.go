// Package config loads and validates the FoxCloud TOML config.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	RolePush   = "push"
	RolePull   = "pull"
	RoleBisync = "bisync"
	RoleMount  = "mount"
)

// DefaultMaxDelete is used when a job leaves max_delete unset. It is low on
// purpose: a sync that wants to delete more than this pauses for a human.
const DefaultMaxDelete = 10

type Config struct {
	Remote  Remote  `toml:"remote"`
	Crypt   Crypt   `toml:"crypt"`
	Daemon  Daemon  `toml:"daemon"`
	Update  Update  `toml:"update"`
	Journal Journal `toml:"journal"`
	Jobs    []Job   `toml:"job"`
}

// DefaultStartupDelay is how long the daemon holds off its first sync pass,
// so boot-time disk activity is not competing with an instantly started sync.
const DefaultStartupDelay = 2 * time.Minute

type Daemon struct {
	// StartupDelay is a Go duration such as "2m"; "0" turns the hold off.
	StartupDelay string `toml:"startup_delay"`
}

// DefaultJournalPath is the shared change log, relative to the SSH login
// folder and outside the synced tree.
const DefaultJournalPath = "foxcloud-journal/journal.log"

// Journal is the shared change log that lets machines learn of each other's
// uploads without scanning the whole remote. Off by default, because it writes
// an (encrypted) file to the server.
type Journal struct {
	Enabled bool `toml:"enabled"`
	// Machine names this machine in the log; defaults to the host name.
	Machine string `toml:"machine"`
	// Path is the log on the server, relative to the SSH login folder.
	Path string `toml:"path"`
}

// MachineName is the configured name, else the host name, as a plain word.
func (j Journal) MachineName() string {
	name := j.Machine
	if name == "" {
		name, _ = os.Hostname()
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = "machine"
	}
	return name
}

// LogPath is the configured path or the default.
func (j Journal) LogPath() string {
	if j.Path == "" {
		return DefaultJournalPath
	}
	return j.Path
}

// Update modes.
const (
	UpdateNotify = "notify" // the default: check and say so, install nothing
	UpdateAuto   = "auto"   // also install when every job is idle, then restart
	UpdateOff    = "off"    // no network request at all
)

// DefaultAutoDelay is how old a release must be before auto mode installs it,
// so a bad release can be pulled before machines pick it up.
const DefaultAutoDelay = 24 * time.Hour

// Update controls how the daemon treats new releases.
type Update struct {
	// Mode is "notify" (default), "auto" or "off".
	Mode string `toml:"mode"`
	// AutoDelay is a Go duration such as "24h"; "0" installs at once.
	AutoDelay string `toml:"auto_delay"`
}

// EffectiveMode is Mode with the default filled in.
func (u Update) EffectiveMode() string {
	if u.Mode == "" {
		return UpdateNotify
	}
	return u.Mode
}

// CheckEnabled reports whether the daemon should look for new releases.
func (u Update) CheckEnabled() bool { return u.EffectiveMode() != UpdateOff }

// AutoDelayDuration returns how old a release must be before auto mode takes it.
func (u Update) AutoDelayDuration() time.Duration {
	if u.AutoDelay == "" {
		return DefaultAutoDelay
	}
	d, _ := time.ParseDuration(u.AutoDelay)
	return d
}

// StartupDelayDuration returns the hold before the first pass.
func (d Daemon) StartupDelayDuration() time.Duration {
	if d.StartupDelay == "" {
		return DefaultStartupDelay
	}
	v, _ := time.ParseDuration(d.StartupDelay)
	return v
}

// Remote describes the SFTP server. Either set SSHHost to a Host entry from
// ~/.ssh/config (resolved with `ssh -G`), or set Host and User directly.
// Fields set explicitly override what the SSH config says.
type Remote struct {
	SSHHost string `toml:"ssh_host"`
	Host    string `toml:"host"`
	Port    int    `toml:"port"`
	User    string `toml:"user"`
	KeyFile string `toml:"key_file"`
	// KnownHostsFile verifies the server's host key. Defaults to the SSH
	// config's UserKnownHostsFile, else ~/.ssh/known_hosts.
	KnownHostsFile string `toml:"known_hosts_file"`
	Path           string `toml:"path"`
}

type Crypt struct {
	EncryptFilenames      bool `toml:"encrypt_filenames"`
	EncryptDirectoryNames bool `toml:"encrypt_directory_names"`
	// KeyCommand yields the master key the crypt password and salt derive from.
	KeyCommand string `toml:"key_command"`
}

type Job struct {
	Name         string `toml:"name"`
	Role         string `toml:"role"`
	LocalPath    string `toml:"local_path"`
	RemotePath   string `toml:"remote_path"`
	Interval     string `toml:"interval"`
	Watch        bool   `toml:"watch"`
	RequireMount string `toml:"require_mount"`
	MaxDelete    int    `toml:"max_delete"`
	BackupDir    string `toml:"backup_dir"`
	FilterFile   string `toml:"filter_file"`

	MountPoint   string `toml:"mount_point"`
	CacheMode    string `toml:"cache_mode"`
	CacheMaxSize string `toml:"cache_max_size"`
}

// IntervalDuration returns the parsed interval, or 0 when none is set.
func (j Job) IntervalDuration() time.Duration {
	d, _ := time.ParseDuration(j.Interval)
	return d
}

// Job returns the job with the given name.
func (c *Config) Job(name string) (Job, bool) {
	for _, j := range c.Jobs {
		if j.Name == name {
			return j, true
		}
	}
	return Job{}, false
}

// EnvVar overrides the default config location, e.g. to keep the config in a
// synced backup folder.
const EnvVar = "FOXCLOUD_CONFIG"

// DefaultPath is where the config lives when no -config flag is given:
// $FOXCLOUD_CONFIG if set, else foxcloud/config.toml in the user config dir.
func DefaultPath() (string, error) {
	if p := os.Getenv(EnvVar); p != "" {
		return ExpandHome(p), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "foxcloud", "config.toml"), nil
}

// Load reads, normalises and validates the config at path.
func Load(path string) (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	c.normalise(filepath.Dir(path))
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) normalise(baseDir string) {
	c.Remote.KeyFile = ExpandHome(c.Remote.KeyFile)
	c.Remote.KnownHostsFile = ExpandHome(c.Remote.KnownHostsFile)
	for i := range c.Jobs {
		j := &c.Jobs[i]
		j.LocalPath = ExpandHome(j.LocalPath)
		j.BackupDir = ExpandHome(j.BackupDir)
		j.MountPoint = ExpandHome(j.MountPoint)
		j.RequireMount = ExpandHome(j.RequireMount)
		j.FilterFile = ExpandHome(j.FilterFile)
		if j.MaxDelete == 0 {
			j.MaxDelete = DefaultMaxDelete
		}
		if j.BackupDir == "" && j.LocalPath != "" {
			j.BackupDir = j.LocalPath + ".foxcloud-backup"
		}
		// A relative filter file is resolved next to the config.
		if j.FilterFile != "" && !filepath.IsAbs(j.FilterFile) {
			j.FilterFile = filepath.Join(baseDir, j.FilterFile)
		}
	}
}

// Validate checks the config for mistakes that would otherwise surface mid-sync.
func (c *Config) Validate() error {
	if c.Remote.SSHHost == "" && (c.Remote.Host == "" || c.Remote.User == "") {
		return fmt.Errorf("[remote] needs ssh_host, or both host and user")
	}
	if c.Daemon.StartupDelay != "" {
		d, err := time.ParseDuration(c.Daemon.StartupDelay)
		if err != nil || d < 0 {
			return fmt.Errorf("[daemon] startup_delay %q is not a duration like \"2m\" or \"0\"", c.Daemon.StartupDelay)
		}
	}
	switch c.Update.EffectiveMode() {
	case UpdateNotify, UpdateAuto, UpdateOff:
	default:
		return fmt.Errorf("[update] mode %q is not one of notify, auto, off", c.Update.Mode)
	}
	if c.Update.AutoDelay != "" {
		d, err := time.ParseDuration(c.Update.AutoDelay)
		if err != nil || d < 0 {
			return fmt.Errorf("[update] auto_delay %q is not a duration like \"24h\" or \"0\"", c.Update.AutoDelay)
		}
	}
	if len(c.Jobs) == 0 {
		return fmt.Errorf("no [[job]] defined")
	}
	seen := map[string]bool{}
	for _, j := range c.Jobs {
		if j.Name == "" {
			return fmt.Errorf("a [[job]] has no name")
		}
		if seen[j.Name] {
			return fmt.Errorf("duplicate job name %q", j.Name)
		}
		seen[j.Name] = true
		switch j.Role {
		case RolePush, RolePull, RoleBisync:
			if j.LocalPath == "" {
				return fmt.Errorf("job %q: local_path is required for role %q", j.Name, j.Role)
			}
		case RoleMount:
			if j.MountPoint == "" {
				return fmt.Errorf("job %q: mount_point is required for role %q", j.Name, j.Role)
			}
			switch j.CacheMode {
			case "", "off", "minimal", "writes", "full":
			default:
				return fmt.Errorf("job %q: cache_mode %q is not one of off, minimal, writes, full", j.Name, j.CacheMode)
			}
		default:
			return fmt.Errorf("job %q: unknown role %q (want push, pull, bisync or mount)", j.Name, j.Role)
		}
		if j.Interval != "" {
			if _, err := time.ParseDuration(j.Interval); err != nil {
				return fmt.Errorf("job %q: bad interval %q: %w", j.Name, j.Interval, err)
			}
		}
		if j.MaxDelete < 0 {
			return fmt.Errorf("job %q: max_delete must not be negative", j.Name)
		}
	}
	return nil
}

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
