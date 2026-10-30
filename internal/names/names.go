// Package names finds file and folder names that will not fit on the remote
// once encrypted. The Storage Box rejects any name over 255 bytes, and rclone
// crypt's standard filename encryption makes a name about 1.6 times longer.
package names

import "strings"

// MaxNameLen is the longest single path segment the remote accepts, in bytes.
const MaxNameLen = 255

// EncryptedLen is the length of a name after rclone crypt's standard
// filename encryption: padded to a multiple of 16 bytes (always at least one
// byte of padding), then base32 without padding.
func EncryptedLen(plainBytes int) int {
	padded := plainBytes/16*16 + 16
	return (padded*8 + 4) / 5
}

// MaxPlainLen is the longest plain name that still fits once encrypted.
func MaxPlainLen() int {
	n := 0
	for EncryptedLen(n+1) <= MaxNameLen {
		n++
	}
	return n
}

func fits(segment string, encrypted bool) bool {
	if encrypted {
		return EncryptedLen(len(segment)) <= MaxNameLen
	}
	return len(segment) <= MaxNameLen
}

// TooLong returns the paths (slash separated, directories ending in "/") in
// which any segment would be rejected by the remote.
func TooLong(paths []string, encryptFiles, encryptDirs bool) []string {
	var bad []string
	for _, p := range paths {
		p = strings.TrimRight(p, "\r")
		if p == "" {
			continue
		}
		isDir := strings.HasSuffix(p, "/")
		segs := strings.Split(strings.TrimSuffix(p, "/"), "/")
		for i, s := range segs {
			last := i == len(segs)-1
			enc := encryptDirs
			if last && !isDir {
				enc = encryptFiles
			}
			if !fits(s, enc) {
				bad = append(bad, p)
				break
			}
		}
	}
	return bad
}
