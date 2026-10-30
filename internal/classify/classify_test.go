package classify

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name   string
		output string
		kind   Kind
		pause  bool
	}{
		{"windows permissions", "ERROR : a.txt: Failed to copy: failed to open source object: Access is denied.", KindPermissions, false},
		{"unix permissions", "open /x: permission denied", KindPermissions, false},
		{"auth", "ssh: handshake failed: ssh: unable to authenticate", KindAuth, false},
		{"network", "dial tcp: i/o timeout", KindNetwork, false},
		{"delete limit", "Safety abort: too many deletes (>5)", KindTooManyDel, true},
		{"case clash", "files differ only by case", KindCaseClash, true},
		{"resync", "Bisync critical error: must run --resync", KindNeedsResync, false},
		{"unknown", "something odd", KindUnknown, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.output)
			if got.Kind != tt.kind || got.Pause != tt.pause {
				t.Errorf("Classify() = {%s pause=%v}, want {%s pause=%v}", got.Kind, got.Pause, tt.kind, tt.pause)
			}
		})
	}
}

// A permissions error must win over the generic bisync abort that follows it,
// which is what made the reference implementation's failures opaque.
func TestPermissionsBeatsBisyncAbort(t *testing.T) {
	out := "ERROR : f: Failed to copy: Access is denied.\nERROR : Bisync aborted."
	if got := Classify(out).Kind; got != KindPermissions {
		t.Errorf("got %s, want %s", got, KindPermissions)
	}
}
