package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/sshconfig"
)

func touch(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMergeRemoteFillsFromSSHConfig(t *testing.T) {
	key, kh := touch(t, "key"), touch(t, "known_hosts")
	got := mergeRemote(config.Remote{SSHHost: "box", Path: "/p"}, sshconfig.Result{
		Host: "h.test", Port: 23, User: "alice",
		IdentityFiles: []string{filepath.Join(t.TempDir(), "missing"), key},
		KnownHosts:    []string{kh},
	})
	if got.Host != "h.test" || got.Port != 23 || got.User != "alice" || got.KeyFile != key || got.KnownHostsFile != kh || got.Path != "/p" {
		t.Errorf("merged = %+v", got)
	}
}

func TestMergeRemoteExplicitWins(t *testing.T) {
	got := mergeRemote(config.Remote{SSHHost: "box", Host: "mine", User: "bob", Port: 2222},
		sshconfig.Result{Host: "h.test", Port: 23, User: "alice"})
	if got.Host != "mine" || got.User != "bob" || got.Port != 2222 {
		t.Errorf("merged = %+v", got)
	}
}

func TestFinaliseRemote(t *testing.T) {
	kh := touch(t, "known_hosts")
	got, err := finaliseRemote(config.Remote{Host: "h", User: "u", KnownHostsFile: kh})
	if err != nil || got.Port != 22 {
		t.Errorf("finalise = %+v, %v", got, err)
	}
	if _, err := finaliseRemote(config.Remote{Host: "h", User: "u", KnownHostsFile: filepath.Join(t.TempDir(), "gone")}); err == nil {
		t.Error("a missing known_hosts file must be an error")
	}
}
