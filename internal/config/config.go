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
	Remote Remote `toml:"remote"`
	Crypt  Crypt  `toml:"crypt"`
	Jobs   []Job  `toml:"job"`
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
