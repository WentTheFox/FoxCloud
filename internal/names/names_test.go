package names

import (
	"reflect"
	"strings"
	"testing"
)

// The 143-byte limit was found by a real upload: names of 144 to 155 bytes
// were rejected with SSH_FX_BAD_MESSAGE, shorter ones were accepted.
func TestMaxPlainLen(t *testing.T) {
	if got := MaxPlainLen(); got != 143 {
		t.Errorf("MaxPlainLen() = %d, want 143", got)
	}
}

func TestEncryptedLen(t *testing.T) {
	tests := map[int]int{0: 26, 1: 26, 15: 26, 16: 52, 143: 231, 144: 256, 148: 256}
	for in, want := range tests {
		if got := EncryptedLen(in); got != want {
			t.Errorf("EncryptedLen(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestTooLong(t *testing.T) {
	ok := strings.Repeat("a", 143)
	long := strings.Repeat("a", 144)
	paths := []string{
		"fine.txt",
		ok, // exactly at the limit
		"dir/" + long,
		long + "/inside.txt", // a long folder name counts too
		"short/",
		"",
	}
	got := TooLong(paths, true, true)
	want := []string{"dir/" + long, long + "/inside.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TooLong() = %v, want %v", got, want)
	}
}

func TestTooLongWithoutEncryption(t *testing.T) {
	long := strings.Repeat("a", 200)
	if got := TooLong([]string{long}, false, false); len(got) != 0 {
		t.Errorf("200-byte plain name should fit when nothing is encrypted, got %v", got)
	}
	if got := TooLong([]string{strings.Repeat("a", 256)}, false, false); len(got) != 1 {
		t.Errorf("256-byte plain name must be rejected, got %v", got)
	}
}

func TestCRLFIsIgnored(t *testing.T) {
	if got := TooLong([]string{"fine.txt\r"}, true, true); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
