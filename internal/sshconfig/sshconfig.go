// Package sshconfig resolves a Host entry from the user's SSH config by asking
// ssh itself (`ssh -G`), so Include, Match and defaults all behave as they do
// for a real connection.
package sshconfig

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Result is the part of the resolved SSH config FoxCloud needs.
type Result struct {
	Host          string
	Port          int
	User          string
	IdentityFiles []string
	KnownHosts    []string
}

// Resolve runs `ssh -G alias` and parses the result.
func Resolve(ctx context.Context, alias string) (Result, error) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		return Result{}, fmt.Errorf("ssh_host needs the ssh command in PATH: %w", err)
	}
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, ssh, "-G", alias)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("ssh -G %s: %w: %s", alias, err, strings.TrimSpace(errOut.String()))
	}
	r := Parse(out.String())
	// ssh -G succeeds for unknown aliases and echoes the alias back as the hostname.
	if r.Host == alias {
		return Result{}, fmt.Errorf("no Host entry named %q found in the SSH config", alias)
	}
	return r, nil
}

// Parse reads `ssh -G` output ("keyword value" per line).
func Parse(out string) Result {
	var r Result
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.ToLower(key) {
		case "hostname":
			r.Host = val
		case "port":
			r.Port, _ = strconv.Atoi(val)
		case "user":
			r.User = val
		case "identityfile":
			r.IdentityFiles = append(r.IdentityFiles, val)
		case "userknownhostsfile":
			r.KnownHosts = append(r.KnownHosts, strings.Fields(val)...)
		}
	}
	return r
}
