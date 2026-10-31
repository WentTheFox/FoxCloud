package journal

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Transport moves journal lines to and from the shared log. SSH is the real
// one; tests use an in-memory one.
type Transport interface {
	// Setup makes the log's folder; it is fine if it already exists.
	Setup(ctx context.Context) error
	// Append adds complete lines (each ending in a newline) to the log. A single
	// call writes in one piece, so lines from two machines never interleave.
	Append(ctx context.Context, data string) error
	// Size is the log's length in bytes; 0 if it does not exist yet.
	Size(ctx context.Context) (int64, error)
	// Follow calls onLine for every complete line from byte offset on, then for
	// each new one as it is written, until ctx ends or the connection drops.
	// end is the offset just past the line.
	Follow(ctx context.Context, offset int64, onLine func(line string, end int64)) error
}

// SSH reaches the log through the server's SSH shell: `dd` appends, `stat`
// measures and `tail -F` follows, so a new record arrives in about a second
// with nothing polling. The Hetzner Storage Box's restricted shell has all
// three.
type SSH struct {
	// Bin is the ssh program ("ssh" if empty).
	Bin string
	// Args select the server: everything up to and including the host, e.g.
	// -o BatchMode=yes -p 23 -l user host.
	Args []string
	// Path is the log file on the server, relative to the login folder.
	Path string
}

var safePath = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// ValidPath reports whether p can be used as the log path: the server's shell
// gets it unquoted, so it is limited to plain characters.
func ValidPath(p string) bool {
	return safePath.MatchString(p) && !strings.Contains(p, "..") && !strings.HasPrefix(p, "-")
}

func (s *SSH) command(ctx context.Context, remote string, extra ...string) *exec.Cmd {
	bin := s.Bin
	if bin == "" {
		bin = "ssh"
	}
	args := append(append([]string(nil), extra...), s.Args...)
	args = append(args, remote)
	return exec.CommandContext(ctx, bin, args...)
}

func (s *SSH) dir() string {
	if i := strings.LastIndex(s.Path, "/"); i > 0 {
		return s.Path[:i]
	}
	return ""
}

func (s *SSH) Setup(ctx context.Context) error {
	if !ValidPath(s.Path) {
		return fmt.Errorf("journal path %q has characters the server shell would mangle", s.Path)
	}
	if d := s.dir(); d != "" {
		// Fails harmlessly when it exists; a real problem shows when appending.
		s.command(ctx, "mkdir "+d).Run()
	}
	return nil
}

func (s *SSH) Append(ctx context.Context, data string) error {
	cmd := s.command(ctx, "dd of="+s.Path+" oflag=append conv=notrunc bs=65536")
	cmd.Stdin = strings.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("appending to the journal: %w: %s", err, lastLine(stderr.String()))
	}
	return nil
}

func (s *SSH) Size(ctx context.Context) (int64, error) {
	var stdout, stderr bytes.Buffer
	cmd := s.command(ctx, "stat -c %s "+s.Path)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(strings.ToLower(stderr.String()+stdout.String()), "no such file") {
			return 0, nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && stdout.Len() == 0 && stderr.Len() > 0 {
			// stat reports a missing file with a non-zero status on some shells.
			if strings.Contains(strings.ToLower(stderr.String()), "cannot") {
				return 0, nil
			}
		}
		return 0, fmt.Errorf("measuring the journal: %w: %s", err, lastLine(stderr.String()))
	}
	n, err := strconv.ParseInt(strings.TrimSpace(stdout.String()), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("measuring the journal: unexpected output %q", strings.TrimSpace(stdout.String()))
	}
	return n, nil
}

func (s *SSH) Follow(ctx context.Context, offset int64, onLine func(string, int64)) error {
	// The keepalives notice a dead connection (a laptop that slept, a network
	// that changed) in about 90 s, and the caller reconnects from its offset.
	cmd := s.command(ctx, fmt.Sprintf("tail -c +%d -F %s", offset+1, s.Path),
		"-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=3")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	r := bufio.NewReader(out)
	pos := offset
	for {
		line, err := r.ReadString('\n')
		if err == nil {
			pos += int64(len(line))
			onLine(strings.TrimRight(line, "\r\n"), pos)
			continue
		}
		cmd.Wait()
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("the journal connection closed: %s", lastLine(stderr.String()))
		}
		return err
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
