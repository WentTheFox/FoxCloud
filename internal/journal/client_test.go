package journal

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// memLog is an in-memory shared log standing in for the server.
type memLog struct {
	mu      sync.Mutex
	data    string
	waiters []chan struct{}
	// down makes Follow fail at once, like a dropped connection.
	down bool
}

func (m *memLog) Setup(context.Context) error { return nil }
func (m *memLog) Append(_ context.Context, d string) error {
	m.mu.Lock()
	m.data += d
	ws := m.waiters
	m.waiters = nil
	m.mu.Unlock()
	for _, w := range ws {
		close(w)
	}
	return nil
}
func (m *memLog) Size(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.data)), nil
}
func (m *memLog) Follow(ctx context.Context, off int64, on func(string, int64)) error {
	pos := off
	for {
		m.mu.Lock()
		if m.down {
			m.mu.Unlock()
			return context.DeadlineExceeded
		}
		rest := ""
		if int(pos) <= len(m.data) {
			rest = m.data[pos:]
		}
		wait := make(chan struct{})
		m.waiters = append(m.waiters, wait)
		m.mu.Unlock()
		for {
			i := strings.IndexByte(rest, '\n')
			if i < 0 {
				break
			}
			pos += int64(i + 1)
			on(rest[:i], pos)
			rest = rest[i+1:]
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wait:
		}
	}
}

func client(t *testing.T, log *memLog, machine, stateDir string) *Client {
	t.Helper()
	codec, err := NewCodec("test master key")
	if err != nil {
		t.Fatal(err)
	}
	return &Client{
		Codec: codec, Transport: log, Machine: machine,
		StateFile:  filepath.Join(stateDir, machine+".json"),
		MinBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
	}
}

type sink struct {
	mu   sync.Mutex
	recs []Record
	gaps []string
}

func (s *sink) handle(r Record) { s.mu.Lock(); s.recs = append(s.recs, r); s.mu.Unlock() }
func (s *sink) gap(why string)  { s.mu.Lock(); s.gaps = append(s.gaps, why); s.mu.Unlock() }
func (s *sink) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recs), len(s.gaps)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func run(t *testing.T, c *Client) (*sink, context.CancelFunc) {
	t.Helper()
	s := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, s.handle, s.gap); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, cancel
}

func TestCodecRoundTripAndTamper(t *testing.T) {
	c, _ := NewCodec("k")
	line, err := c.Encode(Record{Machine: "a", Op: OpUpload, Path: "secret name.txt", Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(line, "secret") {
		t.Error("the path appears in the clear")
	}
	got, err := c.Decode(line)
	if err != nil || got.Path != "secret name.txt" || got.Op != OpUpload {
		t.Errorf("round trip = %+v, %v", got, err)
	}
	other, _ := NewCodec("different key")
	if _, err := other.Decode(line); err != ErrBadLine {
		t.Errorf("a different key must not decrypt: %v", err)
	}
	if _, err := c.Decode(line[:len(line)-4]); err != ErrBadLine {
		t.Errorf("a cut-short line must be rejected: %v", err)
	}
	if _, err := c.Decode("not base64 !!"); err != ErrBadLine {
		t.Errorf("junk must be rejected: %v", err)
	}
}

func TestOneMachineLearnsOfAnothersChanges(t *testing.T) {
	log := &memLog{}
	dir := t.TempDir()
	reader := client(t, log, "laptop", dir)
	s, _ := run(t, reader)
	time.Sleep(50 * time.Millisecond) // the reader is positioned at the end

	writer := client(t, log, "desktop", dir)
	if err := writer.Write(context.Background(), []Record{
		{Op: OpUpload, Path: "a/b.txt", Size: 5}, {Op: OpDelete, Path: "old.txt"},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two records", func() bool { n, _ := s.counts(); return n == 2 })
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recs[0].Path != "a/b.txt" || s.recs[0].Machine != "desktop" || s.recs[1].Op != OpDelete {
		t.Errorf("records = %+v", s.recs)
	}
	if len(s.gaps) != 0 {
		t.Errorf("unexpected gaps: %v", s.gaps)
	}
}

func TestOwnRecordsAreSkipped(t *testing.T) {
	log := &memLog{}
	c := client(t, log, "desktop", t.TempDir())
	s, _ := run(t, c)
	time.Sleep(50 * time.Millisecond)
	c.Write(context.Background(), []Record{{Op: OpUpload, Path: "mine"}})
	time.Sleep(150 * time.Millisecond)
	if n, g := s.counts(); n != 0 || g != 0 {
		t.Errorf("own record handled: %d records, %d gaps", n, g)
	}
}

func TestStartsAtTheEndOfAnExistingLog(t *testing.T) {
	log := &memLog{}
	dir := t.TempDir()
	old := client(t, log, "desktop", dir)
	old.Write(context.Background(), []Record{{Op: OpUpload, Path: "ancient"}})

	s, _ := run(t, client(t, log, "laptop", dir))
	time.Sleep(100 * time.Millisecond)
	if n, _ := s.counts(); n != 0 {
		t.Error("history from before the first run is the full pass's business, not the journal's")
	}
}

func TestReconnectResumesFromTheSavedOffset(t *testing.T) {
	log := &memLog{}
	dir := t.TempDir()
	reader := client(t, log, "laptop", dir)
	s, _ := run(t, reader)
	time.Sleep(50 * time.Millisecond)

	writer := client(t, log, "desktop", dir)
	writer.Write(context.Background(), []Record{{Op: OpUpload, Path: "one"}})
	waitFor(t, "first record", func() bool { n, _ := s.counts(); return n == 1 })

	// The connection drops; the writer carries on meanwhile.
	log.mu.Lock()
	log.down = true
	log.mu.Unlock()
	log.Append(context.Background(), "") // wake the follower so it notices
	writer.Write(context.Background(), []Record{{Op: OpUpload, Path: "two"}, {Op: OpUpload, Path: "three"}})
	time.Sleep(60 * time.Millisecond)
	log.mu.Lock()
	log.down = false
	log.mu.Unlock()

	waitFor(t, "records written while disconnected", func() bool { n, _ := s.counts(); return n == 3 })
	if _, g := s.counts(); g != 0 {
		t.Errorf("a clean catch-up must not raise a gap: %v", s.gaps)
	}
}

func TestMissingRecordsRaiseAGap(t *testing.T) {
	log := &memLog{}
	dir := t.TempDir()
	s, _ := run(t, client(t, log, "laptop", dir))
	time.Sleep(50 * time.Millisecond)

	w := client(t, log, "desktop", dir)
	w.Write(context.Background(), []Record{{Op: OpUpload, Path: "1"}})
	waitFor(t, "first", func() bool { n, _ := s.counts(); return n == 1 })
	// Two records are lost on their way (a failed append): 2 and 3 never arrive.
	w.mu.Lock()
	w.st.Seq += 2
	w.mu.Unlock()
	w.Write(context.Background(), []Record{{Op: OpUpload, Path: "4"}})
	waitFor(t, "a gap", func() bool { _, g := s.counts(); return g == 1 })
	if n, _ := s.counts(); n != 2 {
		t.Errorf("the record after the gap is still delivered, got %d records", n)
	}
}

func TestAGarbledLineRaisesAGap(t *testing.T) {
	log := &memLog{}
	s, _ := run(t, client(t, log, "laptop", t.TempDir()))
	time.Sleep(50 * time.Millisecond)
	log.Append(context.Background(), "this is not a record\n")
	waitFor(t, "a gap", func() bool { _, g := s.counts(); return g == 1 })
}

func TestRestartedMachineRaisesAGap(t *testing.T) {
	log := &memLog{}
	s, _ := run(t, client(t, log, "laptop", t.TempDir()))
	time.Sleep(50 * time.Millisecond)

	first := client(t, log, "desktop", t.TempDir())
	first.Write(context.Background(), []Record{{Op: OpUpload, Path: "x"}})
	waitFor(t, "first", func() bool { n, _ := s.counts(); return n == 1 })
	reborn := client(t, log, "desktop", t.TempDir()) // lost its state: new generation, counter at 1
	reborn.Write(context.Background(), []Record{{Op: OpUpload, Path: "y"}})
	waitFor(t, "a gap", func() bool { _, g := s.counts(); return g == 1 })
}

func TestAResetLogRaisesAGap(t *testing.T) {
	log := &memLog{}
	dir := t.TempDir()
	w := client(t, log, "desktop", dir)
	w.Write(context.Background(), []Record{{Op: OpUpload, Path: "x"}})

	// A reader that had read to the end of a longer log, which is then emptied.
	r := client(t, log, "laptop", dir)
	r.load()
	r.st.Started, r.st.Offset = true, 99999
	r.save()
	s, _ := run(t, r)
	waitFor(t, "a gap", func() bool { _, g := s.counts(); return g == 1 })
}

func TestStatePersistsSequenceNumbers(t *testing.T) {
	log := &memLog{}
	dir := t.TempDir()
	a := client(t, log, "desktop", dir)
	a.Write(context.Background(), []Record{{Op: OpUpload, Path: "1"}, {Op: OpUpload, Path: "2"}})
	b := client(t, log, "desktop", dir) // the same machine after a restart
	b.Write(context.Background(), []Record{{Op: OpUpload, Path: "3"}})

	reader := client(t, log, "laptop", t.TempDir())
	reader.load()
	reader.st.Started = true // read from the very start
	s, _ := run(t, reader)
	waitFor(t, "three records", func() bool { n, _ := s.counts(); return n == 3 })
	if _, g := s.counts(); g != 0 {
		t.Errorf("a restart that kept its state must not look like a gap: %v", s.gaps)
	}
}
