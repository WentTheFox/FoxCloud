package rclone

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
)

// Direction is which way a bisync pass is currently copying, from the point of
// view of this machine: bisync's Path1 is the remote and Path2 the local side.
type Direction string

const (
	Upload   Direction = "upload"   // copying to the remote
	Download Direction = "download" // copying to the local folder
)

var (
	ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	// bisync announces each batch as "- Path2    Do queued copies to    - Path1".
	queued = regexp.MustCompile(`Do queued copies to\s*-?\s*(Path[12])\s*$`)
)

// BisyncDirection reports the direction a bisync log message announces, if it
// announces one. The message is rclone's, with or without colour codes.
func BisyncDirection(msg string) (Direction, bool) {
	m := queued.FindStringSubmatch(ansi.ReplaceAllString(msg, ""))
	if m == nil {
		return "", false
	}
	if m[1] == "Path1" {
		return Upload, true
	}
	return Download, true
}

// jsonLog turns rclone's --use-json-log lines back into plain text for the
// log and the failure classifier, and hands each message to onMsg. Lines that
// are not JSON pass through unchanged.
type jsonLog struct {
	out   io.Writer
	onRec func(LogRecord)
	buf   []byte
}

func (j *jsonLog) Write(p []byte) (int, error) {
	j.buf = append(j.buf, p...)
	for {
		i := bytes.IndexByte(j.buf, '\n')
		if i < 0 {
			break
		}
		line := j.buf[:i]
		j.buf = j.buf[i+1:]
		if err := j.line(line); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

func (j *jsonLog) line(line []byte) error {
	var rec struct {
		Level, Msg, Object string
	}
	if json.Unmarshal(line, &rec) != nil || rec.Msg == "" {
		_, err := j.out.Write(append(line, '\n'))
		return err
	}
	text := rec.Level + ": " + ansi.ReplaceAllString(rec.Msg, "")
	if rec.Object != "" {
		text = rec.Level + ": " + rec.Object + ": " + ansi.ReplaceAllString(rec.Msg, "")
	}
	j.onRec(LogRecord{Level: rec.Level, Object: rec.Object, Msg: rec.Msg})
	_, err := io.WriteString(j.out, text+"\n")
	return err
}
