package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// demandLog records the fake-clock instant of every Demand call, per kind.
type demandLog struct {
	mu    sync.Mutex
	clk   *testClock
	calls map[Kind][]time.Time
}

func (d *demandLog) record(k Kind) int {
	now := d.clk.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.calls == nil {
		d.calls = make(map[Kind][]time.Time)
	}
	d.calls[k] = append(d.calls[k], now)
	return len(d.calls[k])
}

func (d *demandLog) at(k Kind) []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls[k])
}

var rateLimitT0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// A rate limit with a reset pauses every kind on its tracker, probes and
// starts alike, until the reset; a kind on another tracker is untouched.
func TestLoopRateLimitPausesTrackerKinds(t *testing.T) {
	const interval = 10 * time.Second
	reset := rateLimitT0.Add(time.Minute)
	for _, tt := range []struct {
		name         string
		trackers     map[Kind]string
		wantFirst    Kind
		wantStartAt  time.Time
		wantResearch time.Time // first research probe
	}{
		{"shared tracker", map[Kind]string{workKind: "github", researchKind: "github"}, workKind, reset, reset},
		{"separate trackers", map[Kind]string{workKind: "github", researchKind: "forgejo"}, researchKind, rateLimitT0, rateLimitT0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clk := &testClock{now: rateLimitT0}
			dl := &demandLog{clk: clk}
			r := &scriptedRunner{revisions: []string{"rev1"}}
			r.setDemand(workKind, 1)
			r.setDemand(researchKind, 1)
			r.onDemand = func(_ context.Context, k Kind) {
				n := dl.record(k)
				if k == workKind && n == 1 {
					r.setDemandErr(workKind, &forge.RateLimitError{Reset: reset})
				} else if k == workKind {
					r.setDemand(workKind, 1)
				}
			}
			var started []Kind
			var startedAt []time.Time
			r.onStart = func(_ context.Context, req ChildRequest) error {
				started = append(started, req.Kind)
				startedAt = append(startedAt, clk.Now())
				cancel()
				return nil
			}
			cfg := probedConfig(1, interval, workKind, researchKind)
			cfg.Trackers = tt.trackers
			cfg.IdleFloor, cfg.IdleCap = time.Hour, 2*time.Hour
			var buf bytes.Buffer

			Loop(ctx, cfg, r, newTestEmitter(&buf), clk)

			if len(started) != 1 || started[0] != tt.wantFirst || !startedAt[0].Equal(tt.wantStartAt) {
				t.Fatalf("starts = %v at %v, want one %s child at %s", started, startedAt, tt.wantFirst, tt.wantStartAt)
			}
			if got := dl.at(researchKind); len(got) == 0 || !got[0].Equal(tt.wantResearch) {
				t.Fatalf("research probes at %v, want the first at %s", got, tt.wantResearch)
			}
			if got := dl.at(workKind); len(got) < 1 || !got[0].Equal(rateLimitT0) {
				t.Fatalf("work probes at %v, want the first at %s", got, rateLimitT0)
			}
			if tt.name == "shared tracker" {
				if got := dl.at(workKind); len(got) < 2 || got[1].Before(reset) {
					t.Fatalf("work probes at %v, want the re-probe no earlier than the reset %s", got, reset)
				}
			}
		})
	}
}

// A rate limit that names no reset, or one already past, pauses four probe
// intervals rather than re-probing straight into the limit.
func TestLoopRateLimitWithoutResetPausesFourIntervals(t *testing.T) {
	const interval = 10 * time.Second
	for name, mk := range map[string]func(*testClock) error{
		"typed, no reset": func(*testClock) error { return &forge.RateLimitError{} },
		"bare sentinel":   func(*testClock) error { return fmt.Errorf("count: %w", forge.ErrRateLimit) },
		"reset in past":   func(c *testClock) error { return &forge.RateLimitError{Reset: c.Now().Add(-time.Hour)} },
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clk := &testClock{now: rateLimitT0}
			dl := &demandLog{clk: clk}
			r := &scriptedRunner{revisions: []string{"rev1"}}
			r.onDemand = func(_ context.Context, k Kind) {
				if dl.record(k) == 1 {
					r.setDemandErr(k, mk(clk))
					return
				}
				cancel()
			}
			var buf bytes.Buffer

			Loop(ctx, probedConfig(1, interval, workKind), r, newTestEmitter(&buf), clk)

			got := dl.at(workKind)
			if len(got) != 2 || got[1].Sub(got[0]) != 4*interval {
				t.Fatalf("probes at %v, want two, four intervals (%s) apart", got, 4*interval)
			}
		})
	}
}

// Rate limits are a pause, never a failure: any number of them stays clear of
// the breaker, while the same number of other errors trips it.
func TestLoopRateLimitNeverTripsBreaker(t *testing.T) {
	const threshold = 3
	for _, tt := range []struct {
		name     string
		err      error
		wantTrip bool
	}{
		{"rate limited", &forge.RateLimitError{Reset: rateLimitT0.Add(time.Second)}, false},
		{"rate limited, no reset", forge.ErrRateLimit, false},
		{"other error", errors.New("tracker down"), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clk := &testClock{now: rateLimitT0}
			dl := &demandLog{clk: clk}
			r := &scriptedRunner{revisions: []string{"rev1"}}
			r.setDemandErr(workKind, tt.err)
			r.onDemand = func(_ context.Context, k Kind) {
				if dl.record(k) > 4*threshold {
					cancel()
				}
			}
			cfg := probedConfig(1, time.Second, workKind)
			cfg.BreakerThreshold, cfg.BreakerWindow = threshold, time.Hour
			var buf bytes.Buffer

			h := Loop(ctx, cfg, r, newTestEmitter(&buf), clk)

			names := eventNames(decodeEvents(t, &buf))
			if tripped := slices.Contains(names, "breaker_trip"); tripped != tt.wantTrip {
				t.Fatalf("breaker_trip = %v, want %v (halt %v)", tripped, tt.wantTrip, h)
			}
			if (h.Class == HaltBreaker) != tt.wantTrip {
				t.Fatalf("halt = %v, want breaker = %v", h, tt.wantTrip)
			}
			if !tt.wantTrip && slices.Contains(names, "backoff") {
				t.Fatalf("a rate limit backed a slot off: %v", names)
			}
		})
	}
}

// probe_failed and probe_resumed mark transitions only: a repeat of the same
// outcome says nothing. probe_rate_limited fires each time a pause begins, so a
// second pause after the first lapsed announces itself even with no successful
// probe between.
func TestLoopProbeEventsFireOnTransitionsOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{now: rateLimitT0}
	dl := &demandLog{clk: clk}
	r := &scriptedRunner{revisions: []string{"rev1"}}
	var untils []time.Time
	r.onDemand = func(_ context.Context, k Kind) {
		switch n := dl.record(k); n {
		case 1, 2:
			r.setDemandErr(k, errors.New("tracker down"))
		case 3, 4:
			reset := clk.Now().Add(30 * time.Second)
			untils = append(untils, reset)
			r.setDemandErr(k, &forge.RateLimitError{Reset: reset})
		case 5, 6:
			r.setDemand(k, 0)
		default:
			cancel()
		}
	}
	cfg := probedConfig(1, 10*time.Second, workKind)
	cfg.Trackers = map[Kind]string{workKind: "github"}
	var buf bytes.Buffer

	Loop(ctx, cfg, r, newTestEmitter(&buf), clk)

	var got []Event
	for _, ev := range decodeEvents(t, &buf) {
		switch ev.Event {
		case "probe_failed", "probe_rate_limited", "probe_resumed":
			got = append(got, ev)
		}
	}
	if names := eventNames(got); !slices.Equal(names, []string{"probe_failed", "probe_rate_limited", "probe_rate_limited", "probe_resumed"}) {
		t.Fatalf("probe events = %v, want one failed, two rate_limited (one per pause), one resumed", names)
	}
	if got[0].Reason != "demand: tracker down" || got[0].Kind != workKind {
		t.Errorf("probe_failed = %+v", got[0])
	}
	for i := range 2 {
		ev, want := got[1+i], untils[i].UTC().Format(time.RFC3339)
		if ev.Tracker != "github" || ev.Until != want || !strings.HasPrefix(ev.Reason, "demand: ") {
			t.Errorf("probe_rate_limited #%d = %+v, want tracker github, until %s, a demand: reason", i+1, ev, want)
		}
	}
	if got[3].Tracker != "github" || got[3].Kind != workKind {
		t.Errorf("probe_resumed = %+v", got[3])
	}
}

// The status file names the tracker a rate limit paused and when it lifts, and
// every kind on it reports that as its next check.
func TestLoopStatusShowsRateLimitedTracker(t *testing.T) {
	ctx, clk, r := parkedLoopDoubles(t, 1)
	clk.setNow(rateLimitT0)
	reset := rateLimitT0.Add(time.Minute)
	r.setDemandErr(workKind, &forge.RateLimitError{Reset: reset})
	cfg := probedConfig(1, 10*time.Second, workKind, researchKind)
	cfg.Trackers = map[Kind]string{workKind: "github", researchKind: "github"}
	dir := t.TempDir()
	cfg.Status = NewStatusWriter(dir, clk.Now)
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		defer close(done)
		Loop(ctx, cfg, r, newTestEmitter(&buf), clk)
	}()
	defer func() {
		cancel()
		<-done
	}()

	clk.awaitSleep(t, 1)
	report, err := ReadStatus(dir)
	if err != nil || report.Status == nil {
		t.Fatalf("ReadStatus: %v, %v", report.Status, err)
	}
	want := reset.UTC().Format(time.RFC3339)
	if got := report.Status.Trackers; len(got) != 1 || got[0] != (TrackerCheck{Tracker: "github", RateLimitedUntil: want}) {
		t.Fatalf("trackers = %+v, want one github entry paused until %s", got, want)
	}
	for _, c := range report.Status.Checks {
		if c.NextCheck != want {
			t.Errorf("kind %s nextCheck = %q, want %q", c.Kind, c.NextCheck, want)
		}
	}
}

// A sibling already holding a fresh count when another kind's probe is limited
// must not start from it: the pause holds every kind on the tracker, and the
// sibling reports the tracker's reset as its next probe until it ends.
func TestLoopRateLimitHoldsSiblingWithFreshCount(t *testing.T) {
	const interval = 10 * time.Second
	ctx, clk, r := parkedLoopDoubles(t, 1)
	clk.setNow(rateLimitT0)
	reset := rateLimitT0.Add(time.Minute)
	r.setDemand(workKind, 1)
	r.setDemand(researchKind, 1)
	var workProbes int
	r.onDemand = func(_ context.Context, k Kind) {
		if k != workKind {
			return
		}
		if workProbes++; workProbes == 1 {
			r.setDemandErr(workKind, &forge.RateLimitError{Reset: reset})
		} else {
			r.setDemand(workKind, 1)
		}
	}
	// The reservation puts research first in probe order, so it is counted
	// before work's probe hits the limit.
	cfg := probedConfig(1, interval, workKind, researchKind)
	cfg.Trackers = map[Kind]string{workKind: "github", researchKind: "github"}
	cfg.ResearchReservation = 1
	dir := t.TempDir()
	cfg.Status = NewStatusWriter(dir, clk.Now)
	var buf bytes.Buffer
	go Loop(ctx, cfg, r, newTestEmitter(&buf), clk)

	clk.awaitSleep(t, 1)
	report, err := ReadStatus(dir)
	if err != nil || report.Status == nil {
		t.Fatalf("ReadStatus: %v, %v", report.Status, err)
	}
	want := reset.UTC().Format(time.RFC3339)
	var research *KindCheck
	for i, c := range report.Status.Checks {
		if c.Kind == researchKind {
			research = &report.Status.Checks[i]
		}
	}
	if research == nil || research.Ready == nil || *research.Ready != 1 || research.NextProbe != want {
		t.Fatalf("research check = %+v, want a fresh count of 1 and next_probe %s", research, want)
	}

	clk.advanceBy(time.Minute)
	clk.releaseOne(t)
	r.awaitStart(t)
}
