// Package shutdown keeps the machine from shutting down (or suspending) while
// changes are still waiting to be uploaded, and says why to whoever is trying.
package shutdown

// Guard blocks shutdown while held. Hold and Release may be called any number
// of times in any order; they do not nest.
type Guard interface {
	// Hold starts blocking shutdown. reason is shown to the user.
	Hold(reason string)
	// Release stops blocking it.
	Release()
	// Close releases the guard and frees what it uses.
	Close()
}

// Nop is a Guard that does nothing, for platforms without a mechanism and as
// the fallback when the real one cannot be set up.
type Nop struct{}

func (Nop) Hold(string) {}
func (Nop) Release()    {}
func (Nop) Close()      {}
