package rclone

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Usage is the storage quota of the remote, in bytes.
type Usage struct {
	Total, Used, Free int64
}

// UsedPercent is how full the storage is, 0 when the total is unknown.
func (u Usage) UsedPercent() float64 {
	if u.Total <= 0 {
		return 0
	}
	return float64(u.Used) / float64(u.Total) * 100
}

// About asks the SFTP server for its disk usage. It queries the raw sftp
// remote, so the crypt layer (which cannot see sizes) is not involved.
func About(ctx context.Context, bin string, env []string) (Usage, error) {
	out, err := Output(ctx, bin, []string{"about", "--json", strings.ToLower(sftpName) + ":"}, env)
	if err != nil {
		return Usage{}, err
	}
	return ParseAbout(out)
}

// ParseAbout reads the output of `rclone about --json`.
func ParseAbout(b []byte) (Usage, error) {
	var raw struct {
		Total *int64 `json:"total"`
		Used  *int64 `json:"used"`
		Free  *int64 `json:"free"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Usage{}, err
	}
	if raw.Total == nil || raw.Used == nil {
		return Usage{}, errors.New("the server did not report its total and used space")
	}
	u := Usage{Total: *raw.Total, Used: *raw.Used}
	if raw.Free != nil {
		u.Free = *raw.Free
	} else {
		u.Free = u.Total - u.Used
	}
	return u, nil
}
