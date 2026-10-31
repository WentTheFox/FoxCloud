package rclone

import (
	"bytes"
	"strings"
	"testing"
)

func TestBisyncDirection(t *testing.T) {
	cases := []struct {
		msg  string
		want Direction
		ok   bool
	}{
		{"- \x1b[34mPath2\x1b[0m    \x1b[35mDo queued copies to\x1b[0m                - \x1b[36mPath1\x1b[0m", Upload, true},
		{"- Path1    Do queued copies to                - Path2", Download, true},
		{"- Path1    Queue copy to Path2       - file", "", false},
		{"Copied (new)", "", false},
	}
	for _, c := range cases {
		got, ok := BisyncDirection(c.msg)
		if got != c.want || ok != c.ok {
			t.Errorf("BisyncDirection(%q) = %q, %v; want %q, %v", c.msg, got, ok, c.want, c.ok)
		}
	}
}

func TestJSONLogRendersPlainText(t *testing.T) {
	var out strings.Builder
	var msgs []string
	j := &jsonLog{out: &out, onRec: func(r LogRecord) { msgs = append(msgs, r.Msg) }}
	// Split mid-line to check buffering.
	in := `{"level":"error","msg":"\u001b[31mfailed\u001b[0m","object":"a/b"}` + "\nplain line\n"
	j.Write([]byte(in[:20]))
	j.Write([]byte(in[20:]))
	want := "error: a/b: failed\nplain line\n"
	if out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
	if len(msgs) != 1 {
		t.Errorf("onMsg calls = %d, want 1", len(msgs))
	}
}

func TestStatsBlocksTellScanningFromTransferring(t *testing.T) {
	var out bytes.Buffer
	var got []LogRecord
	j := &jsonLog{out: &out, onRec: func(r LogRecord) { got = append(got, r) }}
	for _, line := range []string{
		`{"level":"info","msg":"\nTransferred: 0 B / 0 B","stats":{"bytes":0,"transfers":0,"transferring":[]}}`,
		`{"level":"info","msg":"\nTransferred: 1 MiB / 3 MiB","stats":{"bytes":1048576,"transfers":3,"transferring":[{"name":"big.bin"}]}}`,
		`{"level":"info","msg":"\nTransferred: 3 MiB / 3 MiB","stats":{"bytes":3145728,"transfers":4,"transferring":[]}}`,
		`{"level":"info","object":"a.txt","msg":"Copied (new)"}`,
	} {
		j.Write([]byte(line + "\n"))
	}
	if len(got) != 4 || got[0].Working || !got[1].Working || !got[2].Working || got[3].Stats {
		t.Fatalf("records = %+v", got)
	}
	// A burst of stats blocks shows in the log only as the newest one, written
	// when the run ends; ordinary lines are not held back.
	if s := out.String(); !strings.Contains(s, "Copied (new)") || strings.Contains(s, "Transferred") {
		t.Fatalf("log before the end:\n%s", s)
	}
	j.flush()
	if s := out.String(); strings.Count(s, "Transferred") != 1 || !strings.Contains(s, "3 MiB / 3 MiB") {
		t.Fatalf("log after the end:\n%s", s)
	}
}
