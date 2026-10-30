package main

import "testing"

func TestFormatBytes(t *testing.T) {
	tests := map[int64]string{
		0:                 "0 B",
		1023:              "1023 B",
		1024:              "1.0 KiB",
		1536:              "1.5 KiB",
		25 << 30:          "25.0 GiB",
		1 << 40:           "1.0 TiB",
		(1 << 40) - 1<<30: "1023.0 GiB",
	}
	for in, want := range tests {
		if got := formatBytes(in); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
