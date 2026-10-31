package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const remote = "[remote]\nhost = \"h\"\nuser = \"u\"\n"

func TestLoadDefaults(t *testing.T) {
	c, err := Load(write(t, remote+"[[job]]\nname = \"d\"\nrole = \"push\"\nlocal_path = \"/data\"\nfilter_file = \"f.txt\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	j := c.Jobs[0]
	if j.MaxDelete != DefaultMaxDelete {
		t.Errorf("max_delete = %d, want %d", j.MaxDelete, DefaultMaxDelete)
	}
	if j.BackupDir != "/data.foxcloud-backup" {
		t.Errorf("backup_dir = %q", j.BackupDir)
	}
	if !filepath.IsAbs(j.FilterFile) {
		t.Errorf("filter_file %q should be resolved next to the config", j.FilterFile)
	}
}

func TestRemoteAcceptsSSHHostAlone(t *testing.T) {
	body := "[remote]\nssh_host = \"foxbox\"\n[[job]]\nname=\"a\"\nrole=\"pull\"\nlocal_path=\"/x\"\n"
	if _, err := Load(write(t, body)); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"no remote", "[[job]]\nname=\"a\"\nrole=\"push\"\nlocal_path=\"/x\"\n", "[remote]"},
		{"no jobs", remote, "no [[job]]"},
		{"bad role", remote + "[[job]]\nname=\"a\"\nrole=\"zip\"\n", "unknown role"},
		{"push without path", remote + "[[job]]\nname=\"a\"\nrole=\"push\"\n", "local_path"},
		{"mount without point", remote + "[[job]]\nname=\"a\"\nrole=\"mount\"\n", "mount_point"},
		{"duplicate", remote + "[[job]]\nname=\"a\"\nrole=\"pull\"\nlocal_path=\"/x\"\n[[job]]\nname=\"a\"\nrole=\"pull\"\nlocal_path=\"/y\"\n", "duplicate"},
		{"bad update mode", remote + "[update]\nmode=\"yolo\"\n[[job]]\nname=\"a\"\nrole=\"pull\"\nlocal_path=\"/x\"\n", "[update] mode"},
		{"bad auto delay", remote + "[update]\nauto_delay=\"soon\"\n[[job]]\nname=\"a\"\nrole=\"pull\"\nlocal_path=\"/x\"\n", "auto_delay"},
		{"bad interval", remote + "[[job]]\nname=\"a\"\nrole=\"pull\"\nlocal_path=\"/x\"\ninterval=\"soon\"\n", "interval"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(write(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestDefaultPathEnvOverride(t *testing.T) {
	t.Setenv(EnvVar, "/elsewhere/config.toml")
	got, err := DefaultPath()
	if err != nil || got != "/elsewhere/config.toml" {
		t.Errorf("DefaultPath() = %q, %v", got, err)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "config.example.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateModes(t *testing.T) {
	if (Update{}).EffectiveMode() != UpdateNotify || !(Update{}).CheckEnabled() {
		t.Error("the default must be notify, with checking on")
	}
	if (Update{Mode: UpdateOff}).CheckEnabled() {
		t.Error("off must stop the check")
	}
	if !(Update{Mode: UpdateAuto}).CheckEnabled() {
		t.Error("auto must check")
	}
	if (Update{}).AutoDelayDuration() != DefaultAutoDelay {
		t.Error("the default auto delay is 24h")
	}
	if (Update{AutoDelay: "0"}).AutoDelayDuration() != 0 {
		t.Error("\"0\" installs at once")
	}
}

func TestNamesEncryptedByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	body := "[remote]\nhost = \"h\"\nuser = \"u\"\npath = \"/p\"\n[crypt]\nkey_command = \"echo k\"\n[[job]]\nname = \"j\"\nrole = \"push\"\nlocal_path = \"/x\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Crypt.EncryptFilenames || !c.Crypt.EncryptDirectoryNames {
		t.Errorf("names should be encrypted by default: %+v", c.Crypt)
	}
}

func TestMapDrive(t *testing.T) {
	for _, c := range []struct{ in, goos, want string }{
		{`D:\Data\sub`, "linux", "/mnt/d/Data/sub"},
		{`d:/Data`, "linux", "/mnt/d/Data"},
		{`D:`, "linux", "/mnt/d"},
		{`D:\`, "linux", "/mnt/d"},
		{`D:\Data`, "windows", `D:\Data`},
		{`D:x`, "linux", `D:x`},
		{"/srv/data", "linux", "/srv/data"},
		{"relative/dir", "linux", "relative/dir"},
		{"", "linux", ""},
	} {
		if got := mapDrive(c.in, c.goos); got != c.want {
			t.Errorf("mapDrive(%q, %s) = %q, want %q", c.in, c.goos, got, c.want)
		}
	}
}
