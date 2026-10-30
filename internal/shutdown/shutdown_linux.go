package shutdown

import (
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
)

// logind hands out an inhibitor lock as a file descriptor: while it is open,
// shutdown and suspend are blocked (the desktop shows who is holding them and
// why), and closing it lifts the block. Nothing polls.
type inhibitor struct {
	conn *dbus.Conn
	logf func(string, ...any)

	mu sync.Mutex
	fd *os.File
}

// New connects to logind on the system bus. If that is not possible (no
// systemd, no bus) it returns a guard that does nothing, and says so.
func New(logf func(string, ...any)) Guard {
	conn, err := dbus.SystemBus()
	if err != nil {
		logf("shutdown guard is off: no system bus: %v", err)
		return Nop{}
	}
	return &inhibitor{conn: conn, logf: logf}
}

func (g *inhibitor) Hold(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fd != nil {
		return
	}
	var fd dbus.UnixFD
	err := g.conn.Object("org.freedesktop.login1", "/org/freedesktop/login1").
		Call("org.freedesktop.login1.Manager.Inhibit", 0, "shutdown:sleep", "FoxCloud", reason, "block").
		Store(&fd)
	if err != nil {
		g.logf("could not block shutdown: %v", err)
		return
	}
	g.fd = os.NewFile(uintptr(fd), "foxcloud-inhibitor")
}

func (g *inhibitor) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fd != nil {
		g.fd.Close()
		g.fd = nil
	}
}

func (g *inhibitor) Close() {
	g.Release()
	g.conn.Close()
}
