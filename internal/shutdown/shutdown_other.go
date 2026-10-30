//go:build !linux && !windows

package shutdown

// New returns a guard that does nothing: there is no mechanism here yet.
func New(logf func(string, ...any)) Guard { return Nop{} }
