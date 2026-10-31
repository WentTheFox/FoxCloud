package journal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Client writes this machine's records and follows everyone else's.
type Client struct {
	Codec     *Codec
	Transport Transport
	// Machine names this machine in records, so it can skip its own.
	Machine string
	// StateFile keeps the read position and sequence counters across restarts.
	StateFile string
	Logf      func(format string, args ...any)

	// Reconnect backs off between these, doubling; tests shorten them.
	MinBackoff, MaxBackoff time.Duration

	mu sync.Mutex
	st state
}

type peer struct {
	Gen string `json:"gen"`
	Seq uint64 `json:"seq"`
}

type state struct {
	Gen     string          `json:"gen"`
	Seq     uint64          `json:"seq"`
	Started bool            `json:"started"`
	Offset  int64           `json:"offset"`
	Peers   map[string]peer `json:"peers"`
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// load reads the saved state, making a fresh one (with a new generation) when
// there is none or it cannot be read.
func (c *Client) load() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st.Gen != "" {
		return
	}
	if b, err := os.ReadFile(c.StateFile); err == nil {
		if json.Unmarshal(b, &c.st) == nil && c.st.Gen != "" {
			if c.st.Peers == nil {
				c.st.Peers = map[string]peer{}
			}
			return
		}
	}
	g := make([]byte, 6)
	rand.Read(g)
	c.st = state{Gen: hex.EncodeToString(g), Peers: map[string]peer{}}
}

func (c *Client) save() {
	if c.StateFile == "" {
		return
	}
	b, _ := json.Marshal(c.st)
	os.MkdirAll(filepath.Dir(c.StateFile), 0o755)
	tmp := c.StateFile + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, c.StateFile)
	}
}

// maxAppend keeps each write small enough to reach the server in one piece, so
// two machines appending at once cannot interleave their lines.
const maxAppend = 8 << 10

// Write records changes made by this machine. It is best effort by design: a
// failure only means other machines learn of the change from a full scan.
func (c *Client) Write(ctx context.Context, recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	c.load()
	c.mu.Lock()
	var lines []string
	for _, r := range recs {
		c.st.Seq++
		r.Machine, r.Gen, r.Seq, r.Time = c.Machine, c.st.Gen, c.st.Seq, time.Now().Unix()
		line, err := c.Codec.Encode(r)
		if err != nil {
			c.mu.Unlock()
			return err
		}
		lines = append(lines, line+"\n")
	}
	c.save()
	c.mu.Unlock()

	var chunk strings.Builder
	flush := func() error {
		if chunk.Len() == 0 {
			return nil
		}
		err := c.Transport.Append(ctx, chunk.String())
		chunk.Reset()
		return err
	}
	for _, l := range lines {
		if chunk.Len()+len(l) > maxAppend {
			if err := flush(); err != nil {
				return err
			}
		}
		chunk.WriteString(l)
	}
	return flush()
}

// Run follows the journal until ctx ends, calling handle with each record from
// another machine and gap whenever a change may have been missed (and so a
// full scan is due). It reconnects by itself, resuming from the saved offset
// so nothing written while it was away is lost.
func (c *Client) Run(ctx context.Context, handle func(Record), gap func(reason string)) error {
	c.load()
	if err := c.Transport.Setup(ctx); err != nil {
		return err
	}
	min, max := c.MinBackoff, c.MaxBackoff
	if min == 0 {
		min = 5 * time.Second
	}
	if max == 0 {
		max = time.Minute
	}
	backoff := min

	for ctx.Err() == nil {
		if err := c.position(ctx, gap); err != nil {
			c.logf("journal: %v; retrying in %s", err, backoff)
		} else {
			c.mu.Lock()
			from := c.st.Offset
			c.mu.Unlock()
			began := time.Now()
			err := c.Transport.Follow(ctx, from, func(line string, end int64) {
				c.consume(line, end, handle, gap)
			})
			if ctx.Err() != nil {
				return nil
			}
			c.logf("journal: %v; reconnecting in %s", err, backoff)
			if time.Since(began) > 30*time.Second {
				backoff = min // it had been working
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > max {
			backoff = max
		}
	}
	return nil
}

// position settles where to start reading. A first run starts at the end: the
// start-up full pass covers everything before. A log shorter than the saved
// offset was reset, so changes may have been missed.
func (c *Client) position(ctx context.Context, gap func(string)) error {
	size, err := c.Transport.Size(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case !c.st.Started:
		c.st.Started, c.st.Offset = true, size
		c.save()
	case c.st.Offset > size:
		c.st.Offset = size
		c.save()
		go gap("the journal on the server is shorter than expected; it was reset")
	}
	return nil
}

func (c *Client) consume(line string, end int64, handle func(Record), gap func(string)) {
	rec, err := c.Codec.Decode(line)
	var reason string
	var deliver bool

	c.mu.Lock()
	switch {
	case err != nil:
		reason = "a journal line could not be read"
	case rec.Machine == c.Machine:
		// our own record
	default:
		p, known := c.st.Peers[rec.Machine]
		switch {
		case !known:
		case rec.Gen != p.Gen:
			reason = rec.Machine + " restarted its journal"
		case rec.Seq <= p.Seq:
			c.st.Offset = end // a repeat: already handled
			c.save()
			c.mu.Unlock()
			return
		case rec.Seq != p.Seq+1:
			reason = "records from " + rec.Machine + " are missing"
		}
		c.st.Peers[rec.Machine] = peer{Gen: rec.Gen, Seq: rec.Seq}
		deliver = true
	}
	c.st.Offset = end
	c.save()
	c.mu.Unlock()

	if reason != "" {
		gap(reason)
	}
	if deliver {
		handle(rec)
	}
}

// ErrNotConfigured is returned where a journal was asked for without being set up.
var ErrNotConfigured = errors.New("the journal is not configured")
