package shutdown

import "testing"

func TestHoldRegistersAShutdownBlockReason(t *testing.T) {
	g, err := newGuard()
	if err != nil {
		t.Skip("cannot create a window here:", err)
	}
	defer g.Close()

	if _, err := g.reason(); err == nil {
		t.Error("a reason is registered before anything was held")
	}
	g.Hold("FoxCloud is uploading 3 changes")
	got, err := g.reason()
	if err != nil || got != "FoxCloud is uploading 3 changes" {
		t.Errorf("reason = %q, %v", got, err)
	}
	g.Hold("again") // does not nest or replace
	if got, _ := g.reason(); got != "FoxCloud is uploading 3 changes" {
		t.Errorf("a second Hold changed the reason to %q", got)
	}

	g.Release()
	if got, err := g.reason(); err == nil {
		t.Errorf("reason still registered after Release: %q", got)
	}
	g.Release() // harmless twice
}

func TestWindowProcAnswersTheSessionQuery(t *testing.T) {
	heldFlag.Lock()
	heldFlag.on = false
	heldFlag.Unlock()
	if wndProc(0, wmQueryEndSession, 0, 0) != 1 {
		t.Error("with nothing held, the session may end")
	}
	heldFlag.Lock()
	heldFlag.on = true
	heldFlag.Unlock()
	defer func() { heldFlag.Lock(); heldFlag.on = false; heldFlag.Unlock() }()
	if wndProc(0, wmQueryEndSession, 0, 0) != 0 {
		t.Error("while held, the session must be refused")
	}
}
