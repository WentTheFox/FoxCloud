package secret

import (
	"context"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestGenerateAllAndResolve(t *testing.T) {
	keyring.MockInit()

	if err := GenerateAll(); err != nil {
		t.Fatal(err)
	}
	key, err := Resolve(context.Background(), "keyring:"+CryptKey)
	if err != nil || len(key) < 40 {
		t.Fatalf("key = %q, %v", key, err)
	}

	// A second run must not clobber the existing key.
	if err := GenerateAll(); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("second GenerateAll err = %v, want refusal", err)
	}
	if again, _ := Get(CryptKey); again != key {
		t.Error("key changed after a refused regenerate")
	}
}

func TestResolveMissingAndEmpty(t *testing.T) {
	keyring.MockInit()
	if _, err := Resolve(context.Background(), "keyring:"+CryptKey); err == nil || !strings.Contains(err.Error(), "foxcloud secret generate") {
		t.Errorf("err = %v, want a hint to generate", err)
	}
	if v, err := Resolve(context.Background(), ""); v != "" || err != nil {
		t.Errorf("empty spec = %q, %v", v, err)
	}
}

// Derive is part of the on-disk format: these values must never change, or
// data encrypted with an earlier version becomes unreadable.
func TestDeriveIsStable(t *testing.T) {
	pw, salt, err := Derive("test-master-key")
	if err != nil {
		t.Fatal(err)
	}
	// Cross-checked against an independent HKDF-SHA256 implementation.
	const wantPw = "wbJqQJOAHo37HCrKdtOO8YKb2hi-HKkVFPyccZe65_8"
	const wantSalt = "0lTM9U8uBQa0C_0lN3bqrkzSI8AEC9xmXQSt1caYheQ"
	if pw != wantPw || salt != wantSalt {
		t.Errorf("Derive() = %q, %q", pw, salt)
	}
}

func TestDeriveProperties(t *testing.T) {
	pw, salt, _ := Derive("a")
	pw2, salt2, _ := Derive("a")
	pw3, _, _ := Derive("b")
	if pw != pw2 || salt != salt2 {
		t.Error("Derive is not deterministic")
	}
	if pw == salt {
		t.Error("password and salt must differ")
	}
	if pw == pw3 {
		t.Error("different keys must give different passwords")
	}
	if _, _, err := Derive(""); err == nil {
		t.Error("empty key should be rejected")
	}
}
