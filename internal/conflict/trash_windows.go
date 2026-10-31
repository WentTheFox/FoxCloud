package conflict

import (
	"fmt"
	"syscall"
	"unsafe"
)

// SHFILEOPSTRUCT for SHFileOperationW (64-bit layout; FoxCloud builds amd64
// and arm64 only).
type shFileOp struct {
	Hwnd                 uintptr
	Func                 uint32
	From                 *uint16
	To                   *uint16
	Flags                uint16
	AnyOperationsAborted int32
	NameMappings         uintptr
	ProgressTitle        *uint16
}

const (
	foDelete          = 0x3
	fofSilent         = 0x4
	fofNoConfirmation = 0x10
	fofAllowUndo      = 0x40
	fofNoErrorUI      = 0x400
)

var shFileOperation = syscall.NewLazyDLL("shell32.dll").NewProc("SHFileOperationW")

// MoveToTrash sends a file to the Recycle Bin, where it can be restored.
func MoveToTrash(p string) error {
	// The list of names ends with an extra NUL.
	from, err := syscall.UTF16FromString(p)
	if err != nil {
		return err
	}
	from = append(from, 0)
	op := shFileOp{
		Func:  foDelete,
		From:  &from[0],
		Flags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI,
	}
	r, _, _ := shFileOperation.Call(uintptr(unsafe.Pointer(&op)))
	if r != 0 {
		return fmt.Errorf("moving %s to the Recycle Bin failed (code 0x%x)", p, r)
	}
	if op.AnyOperationsAborted != 0 {
		return fmt.Errorf("moving %s to the Recycle Bin was cancelled", p)
	}
	return nil
}
