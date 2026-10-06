package daemon

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkind"
)

var (
	workKind     = KindOf(dispatchkind.Work)
	researchKind = KindOf(dispatchkind.Research)
)

// probedConfig is testConfig with kinds drawn by Demand every interval.
func probedConfig(slots int, interval time.Duration, kinds ...Kind) Config {
	cfg := testConfig(slots)
	cfg.Kinds = kinds
	cfg.ProbeIntervals = make(map[Kind]time.Duration, len(kinds))
	for _, k := range kinds {
		cfg.ProbeIntervals[k] = interval
	}
	return cfg
}

// startTimes records the fake-clock instant of every RunChild.
type startTimes struct {
	mu    sync.Mutex
	clk   *testClock
	times []time.Time
}

func (s *startTimes) record() {
	now := s.clk.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.times = append(s.times, now)
}

func (s *startTimes) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.times)
}

// offsets returns each start's distance from origin.
func (s *startTimes) offsets(origin time.Time) []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.times))
	for i, ts := range s.times {
		out[i] = ts.Sub(origin)
	}
	return out
}

func TestLoopProbedKindStartsWithinOneIntervalOfDemandAppearing(t *testing.T) {
	const interval = 10 * time.Second
	for _, kind := range []Kind{workKind, researchKind} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
			starts := &startTimes{clk: clk}
			// The tracker gains work right after the second probe answered
			// empty, the worst case for the third probe to find it.
			var lastEmpty time.Time
			r := &scriptedRunner{revisions: []string{"rev1"}}
			r.onDemand = func(_ context.Context, k Kind) {
				switch r.demandCount(k) {
				case 2:
					lastEmpty = clk.Now()
				case 3:
					r.setDemand(k, 1)
				}
			}
			r.onStart = func(context.Context, ChildRequest) error {
				starts.record()
				cancel()
				return nil
			}
			cfg := probedConfig(1, interval, kind)
			// Exit-driven, this kind would idle for IdleFloor after an empty
			// result; the probe is what must be pacing the start instead.
			cfg.IdleFloor, cfg.IdleCap = time.Hour, 2*time.Hour
			var buf bytes.Buffer

			Loop(ctx, cfg, r, newTestEmitter(&buf), clk)

			if starts.len() != 1 {
				t.Fatalf("children started = %d, want 1", starts.len())
			}
			got := starts.times[0].Sub(lastEmpty)
			if got > interval {
				t.Fatalf("child started %s after the last empty probe, want within one probe interval %s", got, interval)
			}
			if r.demandCount(kind) != 3 {
				t.Fatalf("Demand calls = %d, want 3 (two empty probes, then the one that saw work)", r.demandCount(kind))
			}
		})
	}
}

// A window shut at start parks every slot in awaitWindow, before any Decide,
// so no Demand is asked until it opens.
func TestLoopNoDemandWhileAwakeWindowShut(t *testing.T) {
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{
		now:         time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC),
		sleepSignal: make(chan struct{}, 4),
	}
	clk.park()
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.onDemand = func(context.Context, Kind) { cancel() }
	cfg := probedConfig(2, time.Minute, workKind)
	cfg.Awake = win
	var buf bytes.Buffer

	done := make(chan Halt, 1)
	go func() { done <- Loop(ctx, cfg, r, newTestEmitter(&buf), clk) }()

	clk.awaitSleep(t, 2)
	if got := r.demandCount(workKind); got != 0 {
		t.Fatalf("Demand calls = %d while the window is shut, want 0", got)
	}

	clk.step(time.Date(2026, 1, 1, 9, 0, 1, 0, time.UTC))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return after the window opened")
	}
	if got := r.demandCount(workKind); got == 0 {
		t.Fatalf("Demand calls = 0 after the window opened, want the probe to run")
	}
}

// A window that closes while a probe round is in flight parks the slot before
// the next kind is probed: "No probe runs while the Awake window is shut".
func TestLoopNoDemandOnceAwakeWindowClosesMidProbe(t *testing.T) {
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{
		now:         time.Date(2026, 1, 1, 16, 59, 0, 0, time.UTC),
		sleepSignal: make(chan struct{}, 4),
	}
	clk.park()
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.onDemand = func(context.Context, Kind) {
		if r.demandCount(workKind)+r.demandCount(researchKind) == 1 {
			clk.setNow(time.Date(2026, 1, 1, 17, 0, 1, 0, time.UTC))
		}
	}
	cfg := probedConfig(1, time.Minute, workKind, researchKind)
	cfg.Awake = win
	var buf bytes.Buffer

	done := make(chan Halt, 1)
	go func() { done <- Loop(ctx, cfg, r, newTestEmitter(&buf), clk) }()

	// The slot's first sleep is the window parking it: a pool that kept
	// probing would reach the idle sleep only after asking both kinds.
	clk.awaitSleep(t, 1)
	if got := r.demandCount(workKind) + r.demandCount(researchKind); got != 1 {
		t.Fatalf("Demand calls = %d after the window shut mid-probe, want 1", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return after cancel")
	}
}

// Only a free slot decides, so a pool with every slot running a child asks
// the tracker nothing, however stale the count grows.
func TestLoopNoDemandWhileEverySlotBusy(t *testing.T) {
	const slots = 2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.setDemand(workKind, 5)
	r.holdSlots(slots)
	r.announceEachSlot()
	var buf bytes.Buffer

	done := make(chan Halt, 1)
	go func() { done <- Loop(ctx, probedConfig(slots, time.Minute, workKind), r, newTestEmitter(&buf), clk) }()

	r.awaitStart(t)
	r.awaitStart(t)
	before := r.demandCount(workKind)
	if before == 0 {
		t.Fatalf("Demand calls = 0 before any child started, want the probe that started them")
	}

	clk.advanceBy(24 * time.Hour)
	// Nothing can run a probe now: assert a settled count rather than race
	// one. Both slots are inside RunChild, blocked on their release.
	time.Sleep(20 * time.Millisecond)
	if got := r.demandCount(workKind); got != before {
		t.Fatalf("Demand calls = %d with every slot busy, want unchanged %d", got, before)
	}

	cancel()
	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	r.releaseSlot(t, 1, ChildResult{Exit: 0})
	<-done
}

// Slots that all find a kind stale share one Demand call: the first leads and
// the rest join its flight rather than each asking the tracker.
func TestPoolDemandProbeCoalescesConcurrentSlots(t *testing.T) {
	const slots = 3
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := &scriptedRunner{}
	r.setDemand(workKind, 1)
	entered := make(chan struct{})
	release := make(chan struct{})
	r.onDemand = func(context.Context, Kind) {
		close(entered)
		<-release
	}
	var buf bytes.Buffer
	p, pctx := newPool(context.Background(), probedConfig(slots, time.Minute, workKind), r, newTestEmitter(&buf), clk)
	defer p.cancel()
	joined := make(chan struct{}, slots)
	p.onDemandJoin = func() { joined <- struct{}{} }

	var wg sync.WaitGroup
	results := make([]probeResult, slots)
	probe := func(slot int) {
		defer wg.Done()
		results[slot] = p.probe(pctx, slot, []Kind{workKind})
	}
	wg.Add(1)
	go probe(0)
	<-entered
	wg.Add(slots - 1)
	for s := 1; s < slots; s++ {
		go probe(s)
	}
	for i := 0; i < slots-1; i++ {
		select {
		case <-joined:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d slots joined the flight", i, slots-1)
		}
	}
	close(release)
	wg.Wait()

	if got := r.demandCount(workKind); got != 1 {
		t.Fatalf("Demand calls = %d for %d concurrent askers, want exactly 1", got, slots)
	}
	for s, res := range results {
		if res != probeDecideAgain {
			t.Fatalf("slot %d probe result = %v, want probeDecideAgain", s, res)
		}
	}
	if got := p.st.sched.View(workKind, clk.Now()).Ready; got != 1 {
		t.Fatalf("schedule ready = %d, want the leader's count 1", got)
	}
}

// blockedInProbeKind counts goroutines currently inside probeKind: a leader
// parked in Demand and every joiner parked on its flight both show up. Loop
// owns its pool, so a test cannot hang onDemandJoin on it; the stacks are the
// only window onto "all slots have reached the probe" that needs no sleep.
func blockedInProbeKind() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "(*pool).probeKind(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// Slots that all find a kind stale at once share one Demand call, driven
// through Loop: every slot is first seen inside the probe, with the leader's
// Demand held open, before the answer is released.
func TestLoopConcurrentSlotsShareOneDemandProbe(t *testing.T) {
	const slots = 3
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.setDemand(workKind, slots)
	r.holdSlots(slots)
	r.announceEachSlot()
	// Sampled when the first child starts: the baton keeps every sibling
	// from starting before that child claims, and the claim re-probes by
	// design, so only this count measures the slots' shared probe.
	var probesAtFirstStart int
	var sample sync.Once
	announce := r.onStart
	r.onStart = func(ctx context.Context, req ChildRequest) error {
		sample.Do(func() { probesAtFirstStart = r.demandCount(workKind) })
		return announce(ctx, req)
	}
	release := make(chan struct{})
	// Every call parks, so a Loop that failed to coalesce would show up as a
	// second Demand call rather than a hang.
	r.onDemand = func(ctx context.Context, _ Kind) {
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	var buf bytes.Buffer
	done := make(chan Halt, 1)
	go func() { done <- Loop(ctx, probedConfig(slots, time.Hour, workKind), r, newTestEmitter(&buf), clk) }()

	deadline := time.Now().Add(5 * time.Second)
	for blockedInProbeKind() < slots {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d slots reached the probe", blockedInProbeKind(), slots)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	for i := 0; i < slots; i++ {
		r.awaitStart(t)
	}

	if probesAtFirstStart != 1 {
		t.Fatalf("Demand calls = %d for %d slots asking at once, want exactly 1", probesAtFirstStart, slots)
	}

	cancel()
	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 0})
	}
	<-done
}

// A jammed probed kind (Demand says work, the child says none dispatchable)
// backs off floor, 2*floor, ... capped, exactly like an exit-driven one: the
// Demand count alone must not turn a jam into a spawn loop.
func TestLoopJammedProbedKindBacksOffToCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &testClock{now: t0}
	starts := &startTimes{clk: clk}
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 3}}}
	r.setDemand(workKind, 1)
	r.onStart = func(context.Context, ChildRequest) error {
		starts.record()
		if starts.len() == 5 {
			cancel()
		}
		return nil
	}
	cfg := probedConfig(1, time.Hour, workKind)
	cfg.IdleFloor, cfg.IdleCap = time.Second, 4*time.Second
	var buf bytes.Buffer

	Loop(ctx, cfg, r, newTestEmitter(&buf), clk)

	want := []time.Duration{0, time.Second, 3 * time.Second, 7 * time.Second, 11 * time.Second}
	got := starts.offsets(t0)
	if len(got) != len(want) {
		t.Fatalf("start offsets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("start offsets = %v, want %v (waits floor, 2*floor, then capped)", got, want)
		}
	}
	if n := r.demandCount(workKind); n != 1 {
		t.Fatalf("Demand calls = %d, want 1: a jam gate must not trigger re-probes within the interval", n)
	}
}

// A moved tip lifts the jam gate of a probed kind and resets its backoff to
// the floor, same as for an exit-driven kind.
func TestLoopJammedProbedKindLiftsOnMovedTip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &testClock{now: t0}
	starts := &startTimes{clk: clk}
	// Resolutions: child 1, child 2, then the opportunistic one halfway
	// through child 2's 2s gate, which sees the tip move.
	r := &scriptedRunner{revisions: []string{"rev1"}, moved: []bool{false, false, true, false}, results: []ChildResult{{Exit: 3}}}
	r.setDemand(workKind, 1)
	r.onStart = func(context.Context, ChildRequest) error {
		starts.record()
		if starts.len() == 4 {
			cancel()
		}
		return nil
	}
	cfg := probedConfig(1, time.Hour, workKind)
	cfg.IdleFloor, cfg.IdleCap = time.Second, 4*time.Second
	var buf bytes.Buffer

	Loop(ctx, cfg, r, newTestEmitter(&buf), clk)

	// Without the lift the third child waits out its gate to 3s, the fourth
	// to 7s.
	want := []time.Duration{0, time.Second, 2 * time.Second, 3 * time.Second}
	got := starts.offsets(t0)
	if len(got) != len(want) {
		t.Fatalf("start offsets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("start offsets = %v, want %v (lift at 2s, then back to the floor)", got, want)
		}
	}
	var lifted []Kind
	for _, ev := range decodeEvents(t, &buf) {
		if ev.Event == "tip_moved" {
			lifted = ev.Kinds
		}
	}
	if len(lifted) != 1 || lifted[0] != workKind {
		t.Fatalf("tip_moved kinds = %v, want [work]", lifted)
	}
}

// loopJamAnswers runs a probed work kind whose every child jams (exit 3) and
// whose Demand answers one value per probe, the last repeating, until three
// children have started. The jam floor (8s) is far longer than the probe
// interval (1s), so probes land while the first jam is still gated.
func loopJamAnswers(t *testing.T, answers []int) (offsets []time.Duration, events []Event) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &testClock{now: t0}
	starts := &startTimes{clk: clk}
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 3}}}
	r.onDemand = func(_ context.Context, k Kind) {
		n := min(r.demandCount(k), len(answers))
		r.setDemand(k, answers[n-1])
	}
	r.onStart = func(context.Context, ChildRequest) error {
		starts.record()
		if starts.len() == 3 {
			cancel()
		}
		return nil
	}
	cfg := probedConfig(1, time.Second, workKind)
	cfg.IdleFloor, cfg.IdleCap = 8*time.Second, 64*time.Second
	var buf bytes.Buffer

	Loop(ctx, cfg, r, newTestEmitter(&buf), clk)

	return starts.offsets(t0), decodeEvents(t, &buf)
}

func roseEvents(events []Event) (rose []Event) {
	for _, ev := range events {
		if ev.Event == "demand_rose" {
			rose = append(rose, ev)
		}
	}
	return rose
}

// The kind jams at Ready 2; the probe one interval later counts 3, which lifts
// the jam (demand_rose) and starts a child at 1s, well before the 8s gate
// would have ended. The second jam resets to the floor and the steady count 3
// then never lifts it, so the third child waits it out (1s + 8s).
func TestLoopJammedProbedKindLiftsOnRisingDemand(t *testing.T) {
	got, events := loopJamAnswers(t, []int{2, 3})

	if want := []time.Duration{0, time.Second, 9 * time.Second}; !slices.Equal(got, want) {
		t.Fatalf("start offsets = %v, want %v (the rise starts a child at 1s)", got, want)
	}
	rose := roseEvents(events)
	if len(rose) != 1 {
		t.Fatalf("demand_rose events = %+v, want exactly one", rose)
	}
	if ev := rose[0]; ev.Kind != workKind || ev.Slot == nil || ev.Ready == nil || *ev.Ready != 3 {
		t.Fatalf("demand_rose = %+v, want work kind, slot set, ready 3", ev)
	}
}

// A count that held or fell since the jam never lifts it: no demand_rose, and
// no child starts until the 8s gate ends; the next jam's gate has doubled.
func TestLoopJammedProbedKindStaysJammedOnSteadyOrFallingDemand(t *testing.T) {
	for _, tt := range []struct {
		name    string
		answers []int
	}{
		{"steady", []int{2, 2}},
		{"falling", []int{2, 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, events := loopJamAnswers(t, tt.answers)

			if want := []time.Duration{0, 8 * time.Second, 24 * time.Second}; !slices.Equal(got, want) {
				t.Fatalf("start offsets = %v, want %v (no start before the gate ends, which then doubles)", got, want)
			}
			if rose := roseEvents(events); len(rose) > 0 {
				t.Fatalf("unexpected demand_rose: %+v", rose)
			}
		})
	}
}

// A probe counting more than the jam saw, but landing as the 8s gate ends,
// has no live jam to lift: no demand_rose, and the streak survives, so the
// next gate has doubled (8s + 16s) rather than reset to the floor.
func TestLoopRisingDemandAfterGateEndedDoesNotLift(t *testing.T) {
	got, events := loopJamAnswers(t, []int{2, 2, 2, 2, 2, 2, 2, 2, 3})

	if want := []time.Duration{0, 8 * time.Second, 24 * time.Second}; !slices.Equal(got, want) {
		t.Fatalf("start offsets = %v, want %v (the doubled gate survives)", got, want)
	}
	if rose := roseEvents(events); len(rose) > 0 {
		t.Fatalf("unexpected demand_rose: %+v", rose)
	}
}

// RESEARCH_RESERVATION keeps flooring research while both kinds are probed:
// research-first until the reserved seats are taken, then work.
func TestPoolReservationFloorsResearchAlongsideDemand(t *testing.T) {
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := &scriptedRunner{}
	r.setDemand(workKind, 1)
	r.setDemand(researchKind, 1)
	cfg := probedConfig(2, time.Hour, workKind, researchKind)
	cfg.ResearchReservation = 1
	var buf bytes.Buffer
	p, _ := newPool(context.Background(), cfg, r, newTestEmitter(&buf), clk)
	defer p.cancel()
	now := clk.Now()
	p.mutate(func(s *state) []Event {
		for _, k := range cfg.Kinds {
			s.sched, _ = s.sched.Observe(now, DemandProbed{Kind: k, Ready: 1})
		}
		return nil
	})

	if got, _ := p.startChild(0, workKind, "rev1"); got != researchKind {
		t.Fatalf("first child kind = %q, want research: the reservation is unmet", got)
	}
	if got, _ := p.startChild(1, researchKind, "rev1"); got != workKind {
		t.Fatalf("second child kind = %q, want work: the reserved seat is taken", got)
	}
}

// RESEARCH_RESERVATION keeps flooring research while the kinds are probed,
// however plentiful the other kind's Demand: the reserved seats go to research
// first, the rest to work. A single-kind Daemon is the same schedule over one
// kind: the reservation cannot take a seat for a kind that is not configured,
// and the one kind fills every slot. (A single kind starting from its own
// Demand is also pinned by
// TestLoopProbedKindStartsWithinOneIntervalOfDemandAppearing.)
func TestLoopReservationFloorsResearchAlongsideDemand(t *testing.T) {
	for _, tt := range []struct {
		name        string
		kinds       []Kind
		slots       int
		reservation int
		want        map[Kind]int
	}{
		{"one reserved of two", []Kind{workKind, researchKind}, 2, 1, map[Kind]int{researchKind: 1, workKind: 1}},
		{"two reserved of three", []Kind{workKind, researchKind}, 3, 2, map[Kind]int{researchKind: 2, workKind: 1}},
		{"research only", []Kind{researchKind}, 2, 1, map[Kind]int{researchKind: 2}},
		{"dispatch only", []Kind{workKind}, 2, 1, map[Kind]int{workKind: 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
			r := &scriptedRunner{revisions: []string{"rev1"}}
			for _, k := range tt.kinds {
				r.setDemand(k, 10)
			}
			r.holdSlots(tt.slots)
			r.announceEachSlot()
			cfg := probedConfig(tt.slots, time.Hour, tt.kinds...)
			cfg.ResearchReservation = tt.reservation
			var buf bytes.Buffer
			done := make(chan Halt, 1)
			go func() { done <- Loop(ctx, cfg, r, newTestEmitter(&buf), clk) }()

			for i := 0; i < tt.slots; i++ {
				r.awaitStart(t)
			}
			got := make(map[Kind]int)
			for _, c := range r.calls() {
				got[c.Kind]++
			}
			if !maps.Equal(got, tt.want) {
				t.Fatalf("children started by kind = %v, want %v", got, tt.want)
			}

			cancel()
			for s := 0; s < tt.slots; s++ {
				r.releaseSlot(t, s, ChildResult{Exit: 0})
			}
			<-done
		})
	}
}

// A kind whose Demand reads zero yields its turn without a child: work keeps
// every slot while research is empty, and vice versa.
func TestPoolEmptyDemandKindYieldsToTheOther(t *testing.T) {
	for _, tt := range []struct {
		name         string
		work, resrch int
		reservation  int
		want         Kind
	}{
		{"research empty", 1, 0, 1, workKind},
		{"work empty", 0, 1, 0, researchKind},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
			cfg := probedConfig(1, time.Hour, workKind, researchKind)
			cfg.ResearchReservation = tt.reservation
			var buf bytes.Buffer
			p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), clk)
			defer p.cancel()
			now := clk.Now()
			p.mutate(func(s *state) []Event {
				s.sched, _ = s.sched.Observe(now, DemandProbed{Kind: workKind, Ready: tt.work})
				s.sched, _ = s.sched.Observe(now, DemandProbed{Kind: researchKind, Ready: tt.resrch})
				return nil
			})
			if kind, ok := p.pickKind(); !ok || kind != tt.want {
				t.Fatalf("pickKind = (%q, %v), want (%q, true)", kind, ok, tt.want)
			}
		})
	}
}

// demand_appeared/demand_drained mark zero crossings only: the first probe
// finding none says nothing, a count moving between nonzero values says
// nothing, and a steady zero says nothing.
func TestLoopDemandEventsFireOnZeroCrossingsOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	// One answer per probe; the last repeats.
	answers := []int{0, 2, 3, 0, 0, 1, 1}
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
	r.onDemand = func(_ context.Context, k Kind) {
		n := r.demandCount(k)
		if n > len(answers) {
			cancel()
			return
		}
		r.setDemand(k, answers[n-1])
	}
	var buf bytes.Buffer

	Loop(ctx, probedConfig(1, time.Second, workKind), r, newTestEmitter(&buf), clk)

	type crossing struct {
		name  string
		ready int
	}
	var got []crossing
	for _, ev := range decodeEvents(t, &buf) {
		if ev.Event != "demand_appeared" && ev.Event != "demand_drained" {
			continue
		}
		if ev.Kind != workKind || ev.Slot == nil || ev.Ready == nil {
			t.Fatalf("event %+v: want kind, slot and ready set", ev)
		}
		got = append(got, crossing{ev.Event, *ev.Ready})
	}
	want := []crossing{{"demand_appeared", 2}, {"demand_drained", 0}, {"demand_appeared", 1}}
	if len(got) != len(want) {
		t.Fatalf("demand events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("demand events = %v, want %v", got, want)
		}
	}
}

// A child's exit 2 zeroes the scheduling count but is not a probe: the next
// probe must compare against the tracker's last probed count, or a steady
// ready issue re-fires demand_appeared every cycle.
func TestLoopEmptyChildExitDoesNotFakeDemandCrossings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}}}
	r.setDemand(workKind, 1)
	r.onDemand = func(_ context.Context, k Kind) {
		if r.demandCount(k) > 5 {
			cancel()
		}
	}
	var buf bytes.Buffer

	Loop(ctx, probedConfig(1, time.Second, workKind), r, newTestEmitter(&buf), clk)

	if got := demandEventNames(t, &buf); !slices.Equal(got, []string{"demand_appeared"}) {
		t.Fatalf("demand events = %v, want one demand_appeared and no drain", got)
	}
}

// Slots that decided Start before blocking on the discovery baton re-decide
// once they hold it: after the holder's exit 2 zeroed the count, they park
// rather than each spawning an empty child (ADR 0059: a wrong count costs at
// most one spawn).
func TestLoopExtraSlotsDoNotSpawnAfterSiblingExitTwoZeroedDemand(t *testing.T) {
	const slots = 4
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	// Parked, so an idle slot does not fast-forward the clock into a re-probe.
	clk.park()
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	// Deep enough that the start budget (ready minus starting) leaves every
	// sibling a Start to decide.
	r.setDemand(workKind, slots)
	nw := newNotifyWriter()
	done := make(chan Halt, 1)
	go func() { done <- Loop(ctx, probedConfig(slots, time.Hour, workKind), r, newTestEmitter(nw), clk) }()

	lead := r.awaitStart(t)
	// The siblings decided Start off the same count and are now blocked on
	// the baton the lead's child holds: each reports baton_hold once it is
	// committed to waiting for it.
	for i := 0; i < slots-1; i++ {
		nw.waitForLine(t, "\"event\":\"baton_hold\"")
	}
	r.releaseSlot(t, lead, ChildResult{Exit: 2})

	deadline := time.Now().Add(5 * time.Second)
	for clk.waitCount() < slots && r.runCount() == 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Asserts a negative: nothing further may spawn once the siblings have
	// re-decided, so a short settle is the only way to observe it.
	time.Sleep(20 * time.Millisecond)
	if got := r.runCount(); got != 1 {
		t.Fatalf("RunChild calls = %d, want 1: siblings must not spawn after exit 2 zeroed the count", got)
	}

	cancel()
	<-done
}

// A sibling's exit 2 must not swallow the drain: the tracker went 1 to 0 while
// slot 0's child still held the one issue.
func TestLoopEmptyChildExitKeepsRealDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(2)
	r.announceEachSlot()
	r.setDemand(workKind, 1)
	r.onDemand = func(_ context.Context, k Kind) {
		if r.demandCount(k) >= 4 {
			cancel()
		}
	}
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		Loop(ctx, probedConfig(2, time.Second, workKind), r, newTestEmitter(&buf), clk)
	}()

	r.awaitStart(t)
	r.awaitStart(t)
	r.setDemand(workKind, 0)
	r.releaseSlot(t, 1, ChildResult{Exit: 2})
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("slot 1 never re-probed after its exit 2")
	}
	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	<-done

	if got := demandEventNames(t, &buf); !slices.Equal(got, []string{"demand_appeared", "demand_drained"}) {
		t.Fatalf("demand events = %v, want one appeared then one drained", got)
	}
}

func demandEventNames(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	var names []string
	for _, ev := range decodeEvents(t, buf) {
		if ev.Event == "demand_appeared" || ev.Event == "demand_drained" {
			names = append(names, ev.Event)
		}
	}
	return names
}

// A Demand error rests the kind, backs the leader off with a demand: reason,
// and counts toward the breaker like a ResolveTip failure.
func TestLoopDemandErrorCountsTowardBreaker(t *testing.T) {
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.setDemandErr(workKind, errors.New("tracker down"))
	cfg := probedConfig(1, time.Second, workKind)
	cfg.BreakerThreshold = 2
	var buf bytes.Buffer

	h := Loop(context.Background(), cfg, r, newTestEmitter(&buf), clk)

	if h.Class != HaltBreaker {
		t.Fatalf("halt = %v, want the breaker", h)
	}
	if r.runCount() != 0 {
		t.Fatalf("children started = %d, want 0 while Demand fails", r.runCount())
	}
	var reason string
	for _, ev := range decodeEvents(t, &buf) {
		if ev.Event == "backoff" {
			reason = ev.Reason
		}
	}
	if reason != "demand: tracker down" {
		t.Fatalf("backoff reason = %q, want %q", reason, "demand: tracker down")
	}
}

func TestLoopRejectsBadProbeIntervals(t *testing.T) {
	for _, tt := range []struct {
		name string
		mut  func(*Config)
	}{
		{"negative", func(c *Config) { c.ProbeIntervals = map[Kind]time.Duration{workKind: -time.Second} }},
		{"kind not drawn", func(c *Config) { c.ProbeIntervals = map[Kind]time.Duration{researchKind: time.Second} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(1)
			tt.mut(&cfg)
			var buf bytes.Buffer
			h := Loop(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), &testClock{})
			if h.Class != HaltInvalidConfig {
				t.Fatalf("halt = %v, want invalid config", h)
			}
		})
	}
}

func TestLoopRejectsTrackerForUndrawnKind(t *testing.T) {
	cfg := testConfig(1)
	cfg.Trackers = map[Kind]string{researchKind: "github"}
	var buf bytes.Buffer
	h := Loop(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), &testClock{})
	if h.Class != HaltInvalidConfig {
		t.Fatalf("halt = %v, want invalid config", h)
	}
}

// parkedLoopDoubles builds the doubles a wake test needs: a clock that parks
// every Sleep until the test releases it, and a runner holding each slot's
// child until released. The cleanup cancels the returned ctx and drains the
// holds so a Loop the test left running exits.
func parkedLoopDoubles(t *testing.T, slots int) (context.Context, *testClock, *scriptedRunner) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), sleepSignal: make(chan struct{}, 64)}
	clk.park()
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	r.announceEachSlot()
	t.Cleanup(func() {
		cancel()
		for s := 0; s < slots; s++ {
			select {
			case r.release[s] <- ChildResult{}:
			default:
			}
		}
	})
	return ctx, clk, r
}

// requireNoRepark fails unless only the slots' initial parks slept: a woken
// slot starts at once rather than re-parking, and the clock released no sibling.
func requireNoRepark(t *testing.T, clk *testClock, slots int) {
	t.Helper()
	if got := clk.waitCount(); got != slots {
		t.Fatalf("sleeps = %d, want %d: no slot may re-park, and the siblings must not have been released by the clock", got, slots)
	}
}

// One slot's probe finding a backlog wakes every parked sibling at once: the
// siblings are never released by the clock, so only the wake can start them.
func TestLoopProbeFindingBacklogWakesParkedSiblings(t *testing.T) {
	const slots = 3
	const interval = 10 * time.Second
	ctx, clk, r := parkedLoopDoubles(t, slots)
	var buf bytes.Buffer
	go Loop(ctx, probedConfig(slots, interval, workKind), r, newTestEmitter(&buf), clk)

	clk.awaitSleep(t, slots)
	r.setDemand(workKind, slots)
	clk.advanceBy(interval)
	clk.releaseOne(t)

	for i := 0; i < slots; i++ {
		r.awaitStart(t)
	}
	requireNoRepark(t, clk, slots)
}

// A phase change cannot grow the startable set, so a slot's child exiting
// empty leaves its parked siblings asleep.
func TestLoopPhaseChangeDoesNotWakeParkedSiblings(t *testing.T) {
	const slots = 3
	ctx, clk, r := parkedLoopDoubles(t, slots)
	// One item per slot: with fewer, a slot finds the count spoken for by a
	// sibling still starting and parks before the exits under test.
	r.setDemand(workKind, slots)
	var buf bytes.Buffer
	go Loop(ctx, probedConfig(slots, time.Hour, workKind), r, newTestEmitter(&buf), clk)

	// Every slot starts, then each exits empty in turn: the parked ones watch
	// the later slots change phase.
	for i := 0; i < slots; i++ {
		r.awaitStart(t)
	}
	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 2})
		clk.awaitSleep(t, 1)
	}

	// A woken sibling would re-decide and re-park, entering Sleep again.
	select {
	case <-clk.sleepSignal:
		t.Fatalf("a sibling re-parked: the exit woke it though it grew nothing startable")
	case <-time.After(100 * time.Millisecond):
	}
	if got := clk.waitCount(); got != slots {
		t.Fatalf("sleeps = %d, want %d", got, slots)
	}
}

// One slot's opportunistic resolve seeing the tip move lifts the jam and
// wakes every sibling parked on a JamPoll slice: none is released by the
// clock, so only the wake can start them.
func TestLoopJamLiftWakesParkedSiblings(t *testing.T) {
	const slots = 3
	ctx, clk, r := parkedLoopDoubles(t, slots)
	// A tracker that drops one ready issue per claim and keeps `slots` blocked
	// issues: the jam then records `slots`, which every later probe repeats, so
	// only the tip move can lift it, never a rising count.
	r.setDemand(workKind, 2*slots)
	announce := r.onStart
	var claimed atomic.Int32
	r.onStart = func(ctx context.Context, req ChildRequest) error {
		r.setDemand(workKind, 2*slots-int(claimed.Add(1)))
		return announce(ctx, req)
	}
	// The three starting resolves, then a move that repeats: a slot whose
	// pick a sibling's claim spent resolves once more before starting, and a
	// move seen before anything jams lifts nothing, so the opportunistic
	// resolve sees it whichever call it lands on.
	r.moved = []bool{false, false, false, true}
	var buf bytes.Buffer
	go Loop(ctx, probedConfig(slots, time.Hour, workKind), r, newTestEmitter(&buf), clk)

	for i := 0; i < slots; i++ {
		r.awaitStart(t)
	}
	// Each consecutive jam doubles the backoff, so only the last slot to park
	// has a gate longer than its IdleFloor slice and so resolves when its
	// slice ends.
	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 3})
		clk.awaitSleep(t, 1)
	}
	clk.releaseNth(t, slots-1)

	for i := 0; i < slots; i++ {
		r.awaitStart(t)
	}
	requireNoRepark(t, clk, slots)
}

// Demand said there was work and the child exited 2: the next probe skips any
// adapter cache.
func TestLoopEmptyChildAfterDemandForcesFreshProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}}}
	r.setDemand(workKind, 1)
	r.onDemand = func(_ context.Context, k Kind) {
		if r.demandCount(k) > 2 {
			cancel()
		}
	}
	var buf bytes.Buffer

	Loop(ctx, probedConfig(1, time.Second, workKind), r, newTestEmitter(&buf), clk)

	got := r.demandFreshCalls(workKind)
	if len(got) < 2 || got[0] || !got[1] {
		t.Fatalf("fresh flags per probe = %v, want false, true", got)
	}
}
