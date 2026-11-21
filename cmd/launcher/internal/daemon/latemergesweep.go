package daemon

import (
	"context"
	"sync"
	"sync/atomic"
)

// revisionFeed is the newest non-empty revision any resolution returned, moved
// or not (state.tip only follows a move), plus a signal for when the first
// arrives so the late-merge sweeper waits instead of polling.
type revisionFeed struct {
	latest atomic.Value // string; unset until the first set
	first  chan struct{}
	once   sync.Once
}

func newRevisionFeed() *revisionFeed {
	return &revisionFeed{first: make(chan struct{})}
}

func (f *revisionFeed) set(rev string) {
	f.latest.Store(rev)
	f.once.Do(func() { close(f.first) })
}

func (f *revisionFeed) load() string {
	rev, _ := f.latest.Load().(string)
	return rev
}

// firstSeen is closed once a revision has been set.
func (f *revisionFeed) firstSeen() <-chan struct{} { return f.first }

// startLateMergeSweeper runs the late-merge sweep beside the pool, not in it
// (issue #7342), and returns a channel closed once it has stopped.
//
// The first sweep runs as soon as the tip resolves, with no interval wait:
// every Loop restart would reset one, and a daemon restarting more often than
// the interval would never sweep. That is safe because the verb's persistent
// stamp refuses a sweep inside the interval.
//
// poolCtx only schedules new sweeps. A sweep in flight runs on a child of
// callerCtx that the hard-abort latch or a committed in-place handoff also
// cancels, so a pool halt waits it out. A handoff waits for the sweep to exit,
// so no un-reaped process outlives the exec; that interval's claim is lost,
// which the stamp tolerates.
func (p *pool) startLateMergeSweeper(poolCtx, callerCtx context.Context) <-chan struct{} {
	done := make(chan struct{})
	interval := p.cfg.LateMergeSweepInterval
	if interval <= 0 {
		close(done)
		return done
	}
	sweepCtx, cancel := context.WithCancel(callerCtx)
	go func() {
		select {
		case <-p.cfg.Abort:
			cancel()
		case <-p.abandon:
			cancel()
		case <-sweepCtx.Done():
		}
	}()
	go func() {
		defer close(done)
		defer cancel()
		select {
		case <-p.rev.firstSeen():
		case <-poolCtx.Done():
			return
		}
		for poolCtx.Err() == nil {
			revision := p.rev.load()
			if err := p.r.SweepLateMerges(sweepCtx, revision); err != nil && sweepCtx.Err() == nil {
				p.em.warnf("daemon: late-merge sweep failed: %v\n", err)
			}
			p.clk.Sleep(poolCtx, interval)
		}
	}()
	return done
}
