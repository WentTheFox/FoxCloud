package daemon

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/delta"
	"github.com/WentTheFox/FoxCloud/internal/filter"
	"github.com/WentTheFox/FoxCloud/internal/journal"
)

// JournalWriter tells the other machines about changes this one just uploaded.
type JournalWriter func(ctx context.Context, job config.Job, ops delta.Ops) error

// Fetcher downloads named files (paths relative to the job's remote folder)
// into a bisync or pull job's local folder; engine.Engine.Fetch is one.
type Fetcher func(ctx context.Context, job string, paths []string, overwrite bool) error

// IncomingDelay is how long records from other machines are gathered before
// they are acted on, so a burst becomes one fetch.
var IncomingDelay = 2 * time.Second

const journalQueue = 256

type journalWork struct {
	job config.Job
	ops delta.Ops
}

// enqueueJournal hands a batch of applied changes to the single journal writer.
// One writer, in order: sequence numbers must reach the log in the order they
// were given, or readers would see a gap. A full queue drops the entry (other
// machines then learn of it from a full scan) rather than hold uploads back.
func (d *Daemon) enqueueJournal(j config.Job, ops delta.Ops) {
	if d.JournalWrite == nil {
		return
	}
	select {
	case d.journalCh <- journalWork{j, ops}:
	default:
		d.Logf("journal: write queue is full, a change will only be found by a full scan")
	}
}

func (d *Daemon) journalWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case w := <-d.journalCh:
			if err := d.JournalWrite(ctx, w.job, w.ops); err != nil && ctx.Err() == nil {
				d.Logf("journal: could not record changes of job %s: %v", w.job.Name, err)
			}
		}
	}
}

// incoming is what other machines said about one job since it was last acted on.
type incoming struct {
	fetch map[string]bool // files to download
	mkdir map[string]bool // folders to create
	dirty bool            // something a file-by-file download cannot settle
	timer *time.Timer
}

// relTo maps a path in the remote's crypt root to one inside the job's remote
// folder; ok is false when it lies outside it.
func relTo(job config.Job, remotePath string) (string, bool) {
	prefix := strings.Trim(job.RemotePath, "/")
	remotePath = strings.Trim(remotePath, "/")
	switch {
	case prefix == "":
		return remotePath, remotePath != ""
	case remotePath == prefix:
		return "", false
	case strings.HasPrefix(remotePath, prefix+"/"):
		return strings.TrimPrefix(remotePath, prefix+"/"), true
	}
	return "", false
}

// Incoming takes one record another machine wrote. A bisync or pull job
// downloads what is plainly new and otherwise brings its next full pass
// forward; it never acts on a record without that pass or a fetch that checks
// the remote itself.
func (d *Daemon) Incoming(rec journal.Record) {
	for _, j := range d.jobs {
		if j.Role != config.RoleBisync && j.Role != config.RolePull {
			continue
		}
		d.noteIncoming(j, rec)
	}
}

func (d *Daemon) noteIncoming(j config.Job, rec journal.Record) {
	d.inMu.Lock()
	defer d.inMu.Unlock()
	in := d.incoming[j.Name]
	if in == nil {
		in = &incoming{fetch: map[string]bool{}, mkdir: map[string]bool{}}
		d.incoming[j.Name] = in
	}
	rel, inside := relTo(j, rec.Path)
	pull := j.Role == config.RolePull
	switch rec.Op {
	case journal.OpUpload:
		if !inside {
			return
		}
		if pull {
			in.fetch[rel] = true
		} else if _, err := os.Lstat(filepath.Join(j.LocalPath, filepath.FromSlash(rel))); err != nil {
			in.fetch[rel] = true // new here: nothing local to conflict with
		} else {
			in.dirty = true // changed there, and a copy exists here: bisync decides
		}
	case journal.OpMkdir:
		if !inside {
			return
		}
		in.mkdir[rel] = true
	case journal.OpMove:
		to, toInside := relTo(j, rec.To)
		if !inside && !toInside {
			return
		}
		if pull && toInside && !rec.Dir {
			in.fetch[to] = true
		} else {
			in.dirty = true
		}
	case journal.OpDelete, journal.OpRmdir:
		if !inside || pull {
			return // a pull job never deletes
		}
		in.dirty = true
	default:
		return // a record kind this version does not know
	}
	if in.timer == nil {
		in.timer = time.AfterFunc(IncomingDelay, func() { d.flushIncoming(j) })
	} else {
		in.timer.Reset(IncomingDelay)
	}
}

// JournalGap says a change may have been missed, so every job that mirrors the
// remote runs a full pass.
func (d *Daemon) JournalGap(reason string) {
	d.Logf("journal: %s; running full passes to be safe", reason)
	for _, j := range d.jobs {
		if j.Role == config.RoleBisync || j.Role == config.RolePull {
			job := j
			go d.afterFirstPass(job, func() { d.SyncNow(job.Name) })
		}
	}
}

// afterFirstPass runs fn once the job's start-up pass is over: nothing the
// journal says may cut the start-up hold short.
func (d *Daemon) afterFirstPass(j config.Job, fn func()) {
	select {
	case <-d.firstPass[j.Name]:
		fn()
	case <-d.runCtx().Done():
	}
}

func (d *Daemon) flushIncoming(j config.Job) {
	d.afterFirstPass(j, func() { d.handleIncoming(j) })
}

func (d *Daemon) handleIncoming(j config.Job) {
	d.inMu.Lock()
	in := d.incoming[j.Name]
	if in == nil {
		d.inMu.Unlock()
		return
	}
	fetch, mkdir, dirty := in.fetch, in.mkdir, in.dirty
	in.fetch, in.mkdir, in.dirty, in.timer = map[string]bool{}, map[string]bool{}, false, nil
	d.inMu.Unlock()

	ctx := d.runCtx()
	flt, err := filter.Load(j.FilterFile)
	if err != nil {
		flt = &filter.Matcher{}
	}
	for rel := range mkdir {
		if flt.IncludeDir(rel) {
			os.MkdirAll(filepath.Join(j.LocalPath, filepath.FromSlash(path.Clean(rel))), 0o755)
		}
	}
	var paths []string
	for rel := range fetch {
		if flt.Include(rel) {
			paths = append(paths, rel)
		}
	}
	if len(paths) > 0 {
		if d.Fetch == nil {
			dirty = true
		} else {
			d.gate.RLock()
			d.jobMu[j.Name].Lock()
			err := d.Fetch(ctx, j.Name, paths, j.Role == config.RolePull)
			d.jobMu[j.Name].Unlock()
			d.gate.RUnlock()
			if err != nil && ctx.Err() == nil {
				d.Logf("job %s: fetching %d file(s) named by the journal failed (%v); running a full pass", j.Name, len(paths), err)
				dirty = true
			}
		}
	}
	if dirty && ctx.Err() == nil {
		d.SyncNow(j.Name)
	}
}

// runCtx is the context Run was given, or a never-ending one before it starts.
func (d *Daemon) runCtx() context.Context {
	d.ctxMu.Lock()
	defer d.ctxMu.Unlock()
	if d.ctx == nil {
		return context.Background()
	}
	return d.ctx
}

type journalState struct {
	journalCh chan journalWork
	inMu      sync.Mutex
	incoming  map[string]*incoming
	firstPass map[string]chan struct{}
	ctxMu     sync.Mutex
	ctx       context.Context
}
