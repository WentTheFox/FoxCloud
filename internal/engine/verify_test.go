package engine

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func TestPickSample(t *testing.T) {
	paths := []string{"a", "b", "c", "d", "e", "f"}
	r := rand.New(rand.NewPCG(1, 2))

	got := pickSample(paths, 3, r)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p] || !slices.Contains(paths, p) {
			t.Errorf("bad or duplicate pick %q in %v", p, got)
		}
		seen[p] = true
	}

	if all := pickSample(paths, 100, r); len(all) != len(paths) {
		t.Errorf("asking for more than exist should return all %d, got %d", len(paths), len(all))
	}
	if !slices.Equal(paths, []string{"a", "b", "c", "d", "e", "f"}) {
		t.Error("the input slice must not be reordered")
	}
}
