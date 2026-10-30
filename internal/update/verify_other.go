//go:build !windows

package update

// VerifySignatures does nothing off Windows: only the Windows executables are
// signed, and the checksum has already been verified.
func VerifySignatures(map[string]string) error { return nil }
