package rclone

import (
	"os/exec"
	"strings"
	"testing"
)

func TestObscureIsDeterministic(t *testing.T) {
	a, b := Obscure("correct horse"), Obscure("correct horse")
	if a != b {
		t.Errorf("same secret gave different strings: %q vs %q", a, b)
	}
	if a == Obscure("correct horses") {
		t.Error("different secrets gave the same string")
	}
	if strings.Contains(a, "correct") {
		t.Error("secret is visible in the output")
	}
}

// The output must be readable by rclone itself, or the remotes would not open.
func TestObscureRevealsWithRclone(t *testing.T) {
	bin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone not installed")
	}
	for _, plain := range []string{"x", "correct horse", "p4ss/with+odd=chars_-", strings.Repeat("k", 43)} {
		out, err := exec.Command(bin, "reveal", Obscure(plain)).Output()
		if err != nil {
			t.Fatalf("rclone reveal: %v", err)
		}
		if got := strings.TrimSpace(string(out)); got != plain {
			t.Errorf("reveal(Obscure(%q)) = %q", plain, got)
		}
	}
}
