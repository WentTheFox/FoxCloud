package rclone

import (
	"reflect"
	"testing"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/config"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func TestArgs(t *testing.T) {
	base := config.Job{LocalPath: "/data", BackupDir: "/data.bak", MaxDelete: 5, FilterFile: "/f.txt"}
	tail := []string{"--create-empty-src-dirs", "--stats", "1m", "-v", "--filter-from", "/f.txt"}

	tests := []struct {
		name string
		role string
		opts Options
		want []string
	}{
		{"push", config.RolePush, Options{Now: now},
			append([]string{"sync", "/data", "foxcrypt:", "--max-delete", "5"}, tail...)},
		{"pull", config.RolePull, Options{Now: now},
			append([]string{"copy", "foxcrypt:", "/data", "--update", "--backup-dir", "/data.bak", "--suffix", ".20261030-120000"}, tail...)},
		{"bisync", config.RoleBisync, Options{Now: now},
			append([]string{"bisync", "foxcrypt:", "/data", "--resilient", "--recover", "--max-lock", "2m",
				"--conflict-resolve", "newer", "--max-delete", "5", "--backup-dir2", "/data.bak"}, tail...)},
		{"bisync resync", config.RoleBisync, Options{Now: now, Resync: true},
			append([]string{"bisync", "foxcrypt:", "/data", "--resilient", "--recover", "--max-lock", "2m",
				"--conflict-resolve", "newer", "--max-delete", "5", "--backup-dir2", "/data.bak",
				"--resync", "--resync-mode", "newer"}, tail...)},
		{"push dry run force", config.RolePush, Options{Now: now, DryRun: true, Force: true},
			append([]string{"sync", "/data", "foxcrypt:", "--max-delete", "5"}, append(tail, "--dry-run", "--force")...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := base
			j.Role = tt.role
			got, err := Args(j, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Args() =\n%v\nwant\n%v", got, tt.want)
			}
		})
	}
}

func TestArgsRejectsMount(t *testing.T) {
	if _, err := Args(config.Job{Role: config.RoleMount}, Options{Now: now}); err == nil {
		t.Error("expected an error for the mount role")
	}
}

func TestCryptRemote(t *testing.T) {
	if got := CryptRemote("/photos/"); got != "foxcrypt:photos" {
		t.Errorf("got %q", got)
	}
}
