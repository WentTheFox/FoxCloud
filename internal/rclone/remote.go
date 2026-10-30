// Package rclone builds rclone command lines and runs them.
package rclone

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/WentTheFox/FoxCloud/internal/config"
)

// The remotes are defined through environment variables instead of an
// rclone.conf, so no secret is ever written to disk by FoxCloud.
const (
	sftpName  = "FOXSFTP"
	cryptName = "FOXCRYPT"
)

// CryptRemote is the remote string jobs sync against, optionally with a subpath.
func CryptRemote(subpath string) string {
	return strings.ToLower(cryptName) + ":" + strings.Trim(subpath, "/")
}

// Env returns the environment variables that define the sftp and crypt
// remotes. password and salt must already be obscured (see Obscure).
func Env(c *config.Config, password, salt string) []string {
	env := func(remote, key, value string) string {
		return "RCLONE_CONFIG_" + remote + "_" + key + "=" + value
	}
	e := []string{
		env(sftpName, "TYPE", "sftp"),
		env(sftpName, "HOST", c.Remote.Host),
		env(sftpName, "PORT", strconv.Itoa(c.Remote.Port)),
		env(sftpName, "USER", c.Remote.User),
		env(cryptName, "TYPE", "crypt"),
		env(cryptName, "REMOTE", strings.ToLower(sftpName)+":"+strings.Trim(c.Remote.Path, "/")),
		env(cryptName, "PASSWORD", password),
	}
	if c.Remote.KeyFile != "" {
		e = append(e, env(sftpName, "KEY_FILE", c.Remote.KeyFile))
	}
	if c.Remote.KnownHostsFile != "" {
		e = append(e, env(sftpName, "KNOWN_HOSTS_FILE", c.Remote.KnownHostsFile))
	}
	if salt != "" {
		e = append(e, env(cryptName, "PASSWORD2", salt))
	}
	filenames := "off"
	if c.Crypt.EncryptFilenames {
		filenames = "standard"
	}
	e = append(e,
		env(cryptName, "FILENAME_ENCRYPTION", filenames),
		env(cryptName, "DIRECTORY_NAME_ENCRYPTION", strconv.FormatBool(c.Crypt.EncryptDirectoryNames)),
	)
	return e
}

// Obscure converts a plain secret into the form rclone expects in config. The
// secret goes over stdin so it never shows up in a process listing.
func Obscure(ctx context.Context, bin, plain string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, "obscure", "-")
	cmd.Stdin = strings.NewReader(plain)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("rclone obscure: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}
