package update

import (
	"context"
	"math/rand/v2"
	"time"
)

// CheckInterval is the gap between checks, before jitter.
const CheckInterval = 24 * time.Hour

// Announce looks for a newer release after the first delay and then about
// once a day, calling found each time one exists. It uses timers, not polling
// of anything local, and ends when ctx is cancelled. A failed check (offline,
// rate limited) is logged and simply tried again at the next interval.
func Announce(ctx context.Context, current string, first time.Duration, found func(Release), logf func(string, ...any)) {
	wait := first
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		r, err := Latest(ctx)
		switch {
		case err != nil:
			logf("update check failed: %v", err)
		case Newer(current, r.Tag):
			found(r)
		}
		// Jitter keeps every install from asking at the same minute.
		wait = CheckInterval + time.Duration(rand.Int64N(int64(2*time.Hour)))
	}
}
