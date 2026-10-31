package rclone

import (
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
