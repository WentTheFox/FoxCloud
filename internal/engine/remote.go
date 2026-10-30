package engine

import (
	"context"
	"fmt"
	"os"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/sshconfig"
)

// resolveRemote returns the final connection settings: the SSH config entry
// named by ssh_host (if any) overlaid with whatever the config sets explicitly.
func (e *Engine) resolveRemote(ctx context.Context) (config.Remote, error) {
	r := e.Cfg.Remote
	if r.SSHHost != "" {
		res, err := sshconfig.Resolve(ctx, r.SSHHost)
		if err != nil {
			return r, err
		}
		r = mergeRemote(r, res)
	}
	return finaliseRemote(r)
}

// mergeRemote fills the unset fields of r from the resolved SSH config.
// Explicit settings always win.
func mergeRemote(r config.Remote, res sshconfig.Result) config.Remote {
	if r.Host == "" {
		r.Host = res.Host
	}
	if r.User == "" {
		r.User = res.User
	}
	if r.Port == 0 {
		r.Port = res.Port
	}
	if r.KeyFile == "" {
		r.KeyFile = firstExisting(res.IdentityFiles)
	}
	if r.KnownHostsFile == "" {
		r.KnownHostsFile = firstExisting(res.KnownHosts)
	}
	return r
}

// finaliseRemote applies defaults and insists on a known_hosts file: without
// one, rclone would accept any host key and the connection could be intercepted.
func finaliseRemote(r config.Remote) (config.Remote, error) {
	if r.Port == 0 {
		r.Port = 22
	}
	if r.KnownHostsFile == "" {
		r.KnownHostsFile = firstExisting([]string{"~/.ssh/known_hosts"})
	}
	if r.KnownHostsFile == "" {
		return r, fmt.Errorf("no known_hosts file found; connect once with `ssh -p %d %s@%s` to record the server's host key, or set remote.known_hosts_file", r.Port, r.User, r.Host)
	}
	if _, err := os.Stat(r.KnownHostsFile); err != nil {
		return r, fmt.Errorf("known_hosts file: %w", err)
	}
	return r, nil
}

// firstExisting returns the first path (with ~ expanded) that exists.
func firstExisting(paths []string) string {
	for _, p := range paths {
		p = config.ExpandHome(p)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
