package shutdown

import (
	"errors"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows only waits for a process that has a window. So the guard owns a
// hidden top-level window on its own thread with a message loop; while held it
// registers a shutdown block reason (the "this app is preventing shutdown"
// screen shows it) and answers WM_QUERYENDSESSION with "not yet".
var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassEx          = user32.NewProc("RegisterClassExW")
	procCreateWindowEx           = user32.NewProc("CreateWindowExW")
	procDefWindowProc            = user32.NewProc("DefWindowProcW")
	procGetMessage               = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessage          = user32.NewProc("DispatchMessageW")
	procPostMessage              = user32.NewProc("PostMessageW")
	procPostQuitMessage          = user32.NewProc("PostQuitMessage")
	procShutdownBlockReasonMake  = user32.NewProc("ShutdownBlockReasonCreate")
	procShutdownBlockReasonKill  = user32.NewProc("ShutdownBlockReasonDestroy")
	procShutdownBlockReasonQuery = user32.NewProc("ShutdownBlockReasonQuery")
)

const (
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmQueryEndSession = 0x0011
)

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   windows.Handle
	icon       windows.Handle
	cursor     windows.Handle
	background windows.Handle
	menuName   *uint16
	className  *uint16
	iconSm     windows.Handle
}

type point struct{ x, y int32 }

type msg struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type guard struct {
	hwnd windows.Handle
	done chan struct{}

	mu   sync.Mutex
	held bool
}

// held is read by the window procedure, which runs on the window's thread.
var heldFlag struct {
	sync.Mutex
	on bool
}

func wndProc(hwnd windows.Handle, m uint32, wParam, lParam uintptr) uintptr {
	switch m {
	case wmQueryEndSession:
		heldFlag.Lock()
		on := heldFlag.on
		heldFlag.Unlock()
		if on {
			return 0 // FALSE: do not end the session yet
		}
		return 1
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(m), wParam, lParam)
	return r
}

// New creates the hidden window. If that fails the guard does nothing, and
// says so.
func New(logf func(string, ...any)) Guard {
	g, err := newGuard()
	if err != nil {
		logf("shutdown guard is off: %v", err)
		return Nop{}
	}
	return g
}

func newGuard() (*guard, error) {
	type result struct {
		hwnd windows.Handle
		err  error
	}
	res := make(chan result, 1)
	g := &guard{done: make(chan struct{})}
	go func() {
		// A window belongs to the thread that created it, and that thread has
		// to run its messages.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(g.done)

		name, _ := windows.UTF16PtrFromString("FoxCloudShutdownGuard")
		var inst windows.Handle
		windows.GetModuleHandleEx(0, nil, &inst)
		cls := wndClassEx{wndProc: windows.NewCallback(wndProc), instance: inst, className: name}
		cls.size = uint32(unsafe.Sizeof(cls))
		if a, _, e := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&cls))); a == 0 && e != windows.ERROR_CLASS_ALREADY_EXISTS {
			res <- result{err: e}
			return
		}
		title, _ := windows.UTF16PtrFromString("FoxCloud")
		h, _, e := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(title)),
			0, 0, 0, 0, 0, 0, 0, uintptr(inst), 0)
		if h == 0 {
			res <- result{err: e}
			return
		}
		res <- result{hwnd: windows.Handle(h)}

		var m msg
		for {
			r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	r := <-res
	if r.err != nil {
		return nil, r.err
	}
	g.hwnd = r.hwnd
	return g, nil
}

func (g *guard) Hold(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held {
		return
	}
	text, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return
	}
	if r, _, _ := procShutdownBlockReasonMake.Call(uintptr(g.hwnd), uintptr(unsafe.Pointer(text))); r == 0 {
		return
	}
	g.held = true
	heldFlag.Lock()
	heldFlag.on = true
	heldFlag.Unlock()
}

func (g *guard) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.held {
		return
	}
	g.held = false
	heldFlag.Lock()
	heldFlag.on = false
	heldFlag.Unlock()
	procShutdownBlockReasonKill.Call(uintptr(g.hwnd))
}

func (g *guard) Close() {
	g.Release()
	procPostMessage.Call(uintptr(g.hwnd), wmClose, 0, 0)
	<-g.done
}

// reason reads back the block reason registered for the window, for tests.
func (g *guard) reason() (string, error) {
	buf := make([]uint16, 256)
	n := uint32(len(buf))
	r, _, e := procShutdownBlockReasonQuery.Call(uintptr(g.hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		if e == syscall.Errno(0) {
			e = errors.New("no reason registered")
		}
		return "", e
	}
	return windows.UTF16ToString(buf), nil
}
