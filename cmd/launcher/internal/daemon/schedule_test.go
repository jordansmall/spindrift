package daemon

import (
	"reflect"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkind"
)

var (
	schedWork     = KindOf(dispatchkind.Work)
	schedResearch = KindOf(dispatchkind.Research)
	schedButler   = KindOf(dispatchkind.Butler)
	schedT0       = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

const (
	schedFloor    = time.Second
	schedCap      = 4 * time.Second
	schedInterval = 10 * time.Second
)

func schedAt(d time.Duration) time.Time { return schedT0.Add(d) }

// probedSchedule probes work and research and leaves the butler exit-driven.
func probedSchedule(kinds []Kind, reservation int) Schedule {
	return newSchedule(kinds, reservation, schedFloor, schedCap, map[Kind]time.Duration{
		schedWork: schedInterval, schedResearch: schedInterval,
	}, nil)
}

func schedObserve(t *testing.T, s Schedule, now time.Time, evs ...SchedEvent) Schedule {
	t.Helper()
	for _, ev := range evs {
		s, _ = s.Observe(now, ev)
	}
	return s
}

func allKinds() []Kind { return []Kind{schedWork, schedResearch, schedButler} }

func TestScheduleDecide(t *testing.T) {
	running := func(research int) Occupancy {
		return Occupancy{Running: map[Kind]int{schedResearch: research}}
	}
	// startingRunning is n children of kind, all still unclaimed: Running as
	// well as Starting, which the reservation floor reads.
	startingRunning := func(kind Kind, n int) Occupancy {
		return Occupancy{Running: map[Kind]int{kind: n}, Starting: map[Kind]int{kind: n}}
	}
	probed := func(s Schedule, now time.Time, kind Kind, ready int) Schedule {
		return schedObserve(t, s, now, DemandProbed{Kind: kind, Ready: ready})
	}

	tests := []struct {
		name  string
		build func() Schedule
		now   time.Duration
		occ   Occupancy
		want  Decision
	}{
		{
			name:  "never probed: probe every probed kind ahead of the butler",
			build: func() Schedule { return probedSchedule(allKinds(), 0) },
			want:  Probe{Kinds: []Kind{schedWork, schedResearch}},
		},
		{
			name: "fresh ready work starts",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 0), schedT0, schedWork, 2)
				return probed(s, schedT0, schedResearch, 0)
			},
			want: Start{Kind: schedWork},
		},
		{
			name: "stale kind ahead of a startable kind probes only the stale one",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 0), schedT0, schedWork, 0)
				return probed(s, schedT0.Add(schedInterval), schedResearch, 1)
			},
			now:  schedInterval,
			want: Probe{Kinds: []Kind{schedWork}},
		},
		{
			name: "stale kind behind a startable kind is not probed",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 0), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			now:  schedInterval - 1,
			want: Start{Kind: schedWork},
		},
		{
			name: "demand exactly one interval old is stale",
			build: func() Schedule {
				s := probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 3)
				return s
			},
			now:  schedInterval,
			want: Probe{Kinds: []Kind{schedWork}},
		},
		{
			name: "fresh ready 0 parks until the next probe",
			build: func() Schedule {
				s := probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 0)
				return s
			},
			now:  3 * time.Second,
			want: Park{Until: schedAt(schedInterval)},
		},
		{
			name: "all drained parks at the earliest next probe",
			build: func() Schedule {
				s := probed(probedSchedule([]Kind{schedWork, schedResearch}, 0), schedT0, schedWork, 0)
				return probed(s, schedAt(4*time.Second), schedResearch, 0)
			},
			now:  5 * time.Second,
			want: Park{Until: schedAt(schedInterval)},
		},
		{
			name: "unprobed butler starts when nothing else has work",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 0), schedT0, schedWork, 0)
				return probed(s, schedT0, schedResearch, 0)
			},
			want: Start{Kind: schedButler},
		},
		{
			name: "butler is last even when probed kinds are all ready",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 5), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			want: Start{Kind: schedResearch},
		},
		{
			name: "no reservation: normal tier before reserved",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 0), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			want: Start{Kind: schedWork},
		},
		{
			name: "reservation unmet: research first",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 1), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			occ:  running(0),
			want: Start{Kind: schedResearch},
		},
		{
			name: "reservation met: work first again",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 1), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			occ:  running(1),
			want: Start{Kind: schedWork},
		},
		{
			name: "reservation unmet but research drained: work starts",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 1), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 0)
			},
			occ:  running(0),
			want: Start{Kind: schedWork},
		},
		{
			name: "start budget spent: ready 1 starting 1 parks until the next probe",
			build: func() Schedule {
				return probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 1)
			},
			occ:  startingRunning(schedWork, 1),
			now:  3 * time.Second,
			want: Park{Until: schedAt(schedInterval)},
		},
		{
			name: "start budget left: ready 3 starting 2 starts",
			build: func() Schedule {
				return probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 3)
			},
			occ:  startingRunning(schedWork, 2),
			want: Start{Kind: schedWork},
		},
		{
			name: "start budget over-spent against a lower count still does not start",
			build: func() Schedule {
				return probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 1)
			},
			occ:  startingRunning(schedWork, 2),
			want: Park{Until: schedAt(schedInterval)},
		},
		{
			name: "start budget spent: the next kind in priority order starts",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 0), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			occ:  startingRunning(schedWork, 1),
			want: Start{Kind: schedResearch},
		},
		{
			name: "start budget is per kind: starting research leaves work its budget",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 0), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			occ:  startingRunning(schedResearch, 1),
			want: Start{Kind: schedWork},
		},
		{
			name: "start budget spent on reserved kind with floor unmet: work starts",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 2), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			occ:  startingRunning(schedResearch, 1),
			want: Start{Kind: schedWork},
		},
		{
			name: "reservation met, work budget spent: research starts",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 1), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 2)
			},
			occ: Occupancy{
				Running:  map[Kind]int{schedWork: 1, schedResearch: 1},
				Starting: map[Kind]int{schedWork: 1},
			},
			want: Start{Kind: schedResearch},
		},
		{
			name: "reservation met, work budget left: work starts ahead of research",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 1), schedT0, schedWork, 2)
				return probed(s, schedT0, schedResearch, 2)
			},
			occ: Occupancy{
				Running:  map[Kind]int{schedWork: 1, schedResearch: 1},
				Starting: map[Kind]int{schedWork: 1},
			},
			want: Start{Kind: schedWork},
		},
		{
			name: "a starting research child alone meets the floor: work starts first",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 1), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 2)
			},
			occ:  startingRunning(schedResearch, 1),
			want: Start{Kind: schedWork},
		},
		{
			name: "start budget spent on both probed kinds: the butler starts",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 2), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			occ: Occupancy{
				Running:  map[Kind]int{schedWork: 1, schedResearch: 1},
				Starting: map[Kind]int{schedWork: 1, schedResearch: 1},
			},
			want: Start{Kind: schedButler},
		},
		{
			name: "start budget spent does not hide a stale kind behind it from probing",
			build: func() Schedule {
				s := probed(probedSchedule([]Kind{schedWork, schedResearch}, 0), schedT0, schedWork, 1)
				return probed(s, schedT0.Add(-schedInterval), schedResearch, 1)
			},
			occ:  startingRunning(schedWork, 1),
			want: Probe{Kinds: []Kind{schedResearch}},
		},
		{
			name: "start budget spent on a stale kind probes it rather than starting",
			build: func() Schedule {
				return probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 1)
			},
			occ:  startingRunning(schedWork, 1),
			now:  schedInterval,
			want: Probe{Kinds: []Kind{schedWork}},
		},
		{
			name: "unprobed kind ignores the start budget",
			build: func() Schedule {
				return newSchedule([]Kind{schedButler}, 0, schedFloor, schedCap, nil, nil)
			},
			occ:  startingRunning(schedButler, 3),
			want: Start{Kind: schedButler},
		},
		{
			name: "jammed kind parks until the jam ends with TipPoll",
			build: func() Schedule {
				s := probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 3)
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildJammed})
			},
			now:  500 * time.Millisecond,
			want: Park{Until: schedAt(schedFloor), TipPoll: true},
		},
		{
			name: "jam gate does not hide a stale kind from probing",
			build: func() Schedule {
				s := newSchedule([]Kind{schedWork}, 0, schedFloor, schedCap, map[Kind]time.Duration{schedWork: 500 * time.Millisecond}, nil)
				s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 3})
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildJammed})
			},
			now:  600 * time.Millisecond,
			want: Probe{Kinds: []Kind{schedWork}},
		},
		{
			name: "jammed kind with a probe due before the jam ends parks until the probe",
			build: func() Schedule {
				s := newSchedule([]Kind{schedWork}, 0, schedFloor, schedCap, map[Kind]time.Duration{schedWork: 500 * time.Millisecond}, nil)
				s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 3})
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildJammed})
			},
			now:  100 * time.Millisecond,
			want: Park{Until: schedAt(500 * time.Millisecond), TipPoll: true},
		},
		{
			name: "jammed kind with a probe due after the jam ends parks until the jam ends",
			build: func() Schedule {
				s := newSchedule([]Kind{schedWork}, 0, schedFloor, schedCap, map[Kind]time.Duration{schedWork: 2 * schedFloor}, nil)
				s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 3})
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildJammed})
			},
			now:  100 * time.Millisecond,
			want: Park{Until: schedAt(schedFloor), TipPoll: true},
		},
		{
			name: "jam elapsed with fresh demand starts again",
			build: func() Schedule {
				s := probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 3)
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildJammed})
			},
			now:  schedFloor,
			want: Start{Kind: schedWork},
		},
		{
			name: "jammed work does not gate a ready sibling",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 0), schedT0, schedWork, 3)
				s = probed(s, schedT0, schedResearch, 1)
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildJammed})
			},
			want: Start{Kind: schedResearch},
		},
		{
			name: "single unprobed kind: never gated, starts",
			build: func() Schedule {
				return newSchedule([]Kind{schedButler}, 0, schedFloor, schedCap, nil, nil)
			},
			want: Start{Kind: schedButler},
		},
		{
			name: "single unprobed kind: empty exit parks on the backoff",
			build: func() Schedule {
				s := newSchedule([]Kind{schedWork}, 0, schedFloor, schedCap, nil, nil)
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildEmpty})
			},
			want: Park{Until: schedAt(schedFloor)},
		},
		{
			name: "single unprobed kind: jammed exit parks with TipPoll",
			build: func() Schedule {
				s := newSchedule([]Kind{schedWork}, 0, schedFloor, schedCap, nil, nil)
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildJammed})
			},
			want: Park{Until: schedAt(schedFloor), TipPoll: true},
		},
		{
			name: "single probed kind never probed",
			build: func() Schedule {
				return probedSchedule([]Kind{schedResearch}, 1)
			},
			occ:  running(0),
			want: Probe{Kinds: []Kind{schedResearch}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.build().Decide(schedAt(tc.now), tc.occ); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Decide = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestScheduleUnprobedKeepsExitDrivenBackoff(t *testing.T) {
	s := newSchedule([]Kind{schedButler}, 0, schedFloor, schedCap, nil, nil)
	now := schedT0
	var waits []time.Duration
	for i := 0; i < 5; i++ {
		// Exit 2 and 3 alike grow the same backoff.
		res := ChildEmpty
		if i%2 == 1 {
			res = ChildJammed
		}
		s = schedObserve(t, s, now, ChildDone{Kind: schedButler, Result: res, GateGen: s.gateGenOf(schedButler)})
		p := s.Decide(now, Occupancy{}).(Park)
		waits = append(waits, p.Until.Sub(now))
		now = p.Until
	}
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}

	s = schedObserve(t, s, now, ChildDone{Kind: schedButler, Result: ChildContinue})
	if d := s.Decide(now, Occupancy{}); d != (Start{Kind: schedButler}) {
		t.Fatalf("after Continue: %#v, want Start", d)
	}
	s = schedObserve(t, s, now, ChildDone{Kind: schedButler, Result: ChildEmpty, GateGen: s.gateGenOf(schedButler)})
	if p := s.Decide(now, Occupancy{}).(Park); p.Until.Sub(now) != schedFloor {
		t.Fatalf("backoff after Continue reset waits %v, want floor", p.Until.Sub(now))
	}
}

func TestScheduleTipMovedResetsOnlyJammedKinds(t *testing.T) {
	s := newSchedule(allKinds(), 0, schedFloor, schedCap, nil, nil)
	s = schedObserve(t, s, schedT0,
		ChildDone{Kind: schedWork, Result: ChildJammed},
		ChildDone{Kind: schedResearch, Result: ChildEmpty},
		ChildDone{Kind: schedButler, Result: ChildJammed})

	s, lifted, _ := s.TipMoved(schedT0)
	if want := []Kind{schedWork, schedButler}; !reflect.DeepEqual(lifted, want) {
		t.Fatalf("lifted = %v, want %v", lifted, want)
	}
	if d := s.Decide(schedT0, Occupancy{}); d != (Start{Kind: schedWork}) {
		t.Fatalf("after lift: %#v, want Start work", d)
	}
	if v := s.View(schedResearch, schedT0); !v.Gated {
		t.Fatal("queue-empty research must stay gated across a moved tip")
	}
	if _, lifted, _ = s.TipMoved(schedT0); len(lifted) != 0 {
		t.Fatalf("second lift = %v, want none", lifted)
	}
}

func TestScheduleJamBackoffDoublesAndTipMovedLifts(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 5})
	now := schedT0
	var waits []time.Duration
	for i := 0; i < 4; i++ {
		s = schedObserve(t, s, now, DemandProbed{Kind: schedWork, Ready: 5}, ChildDone{Kind: schedWork, Result: ChildJammed})
		p := s.Decide(now, Occupancy{}).(Park)
		if !p.TipPoll {
			t.Fatalf("jam %d: TipPoll false", i)
		}
		waits = append(waits, p.Until.Sub(now))
		now = p.Until
	}
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("jam waits = %v, want %v", waits, want)
	}

	s = schedObserve(t, s, now, DemandProbed{Kind: schedWork, Ready: 5}, ChildDone{Kind: schedWork, Result: ChildJammed})
	s, _, _ = s.TipMoved(now)
	if d := s.Decide(now, Occupancy{}); d != (Start{Kind: schedWork}) {
		t.Fatalf("after TipMoved: %#v, want Start", d)
	}
	s = schedObserve(t, s, now, ChildDone{Kind: schedWork, Result: ChildJammed})
	if p := s.Decide(now, Occupancy{}).(Park); p.Until.Sub(now) != schedFloor {
		t.Fatalf("jam after lift waits %v, want floor", p.Until.Sub(now))
	}
}

// A jam lifts on a moved tip or on a probe counting more than the jam saw
// (someone labelled more work); a count that held or fell never lifts it.
func TestScheduleJamLiftsOnDemandRiseOrTipMove(t *testing.T) {
	const readyAtJam, noProbe = 2, -1
	for _, tt := range []struct {
		name     string
		probed   int
		tipMoved bool
		wantJam  bool
	}{
		{"count rises", 3, false, false},
		{"count rises by a lot", 9, false, false},
		{"count holds", 2, false, true},
		{"count falls", 1, false, true},
		{"count drains", 0, false, true},
		{"tip moves, no probe", noProbe, true, false},
		{"tip moves, count holds", 2, true, false},
		{"tip moves, count falls", 1, true, false},
		{"tip moves, count rises", 3, true, false},
		{"nothing moves", noProbe, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := probedSchedule([]Kind{schedWork}, 0)
			s = schedObserve(t, s, schedT0,
				DemandProbed{Kind: schedWork, Ready: readyAtJam},
				ChildDone{Kind: schedWork, Result: ChildJammed})
			if !s.View(schedWork, schedT0).Jammed {
				t.Fatal("setup: kind not jammed")
			}
			now := schedAt(schedFloor / 2)
			if tt.probed != noProbe {
				s = schedObserve(t, s, now, DemandProbed{Kind: schedWork, Ready: tt.probed})
			}
			if tt.tipMoved {
				s, _, _ = s.TipMoved(now)
			}
			if got := s.View(schedWork, now).Jammed; got != tt.wantJam {
				t.Fatalf("Jammed = %v, want %v", got, tt.wantJam)
			}
			// A lifted jam is startable; one still gated is not.
			if _, started := s.Decide(now, Occupancy{}).(Start); started == tt.wantJam {
				t.Fatalf("Decide started = %v, want %v", started, !tt.wantJam)
			}
		})
	}
}

// A rise lifts only a jam still gating: once the gate's wait has passed there
// is nothing to lift, and resetting would wipe the doubling streak.
func TestScheduleDemandRiseAfterGateExpiredKeepsStreak(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0,
		DemandProbed{Kind: schedWork, Ready: 2},
		ChildDone{Kind: schedWork, Result: ChildJammed})
	now := schedAt(schedFloor * 2)
	s, woke := s.Observe(now, DemandProbed{Kind: schedWork, Ready: 3})
	if woke {
		t.Fatal("a rise after the gate ended woke the kind")
	}
	s = schedObserve(t, s, now, ChildDone{Kind: schedWork, Result: ChildJammed})
	if got := s.View(schedWork, now).JamUntil.Sub(now); got != 2*schedFloor {
		t.Fatalf("next jam waits %v, want the doubled %v", got, 2*schedFloor)
	}
}

// A probe read before a claim moved the count must not lift a jam recorded
// after it: the higher count it carries is the pre-claim one.
func TestScheduleDemandRiseFromProbeThatRacedAClaimDoesNotLift(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0,
		DemandProbed{Kind: schedWork, Ready: 3},
		Claimed{Kind: schedWork},
		ChildDone{Kind: schedWork, Result: ChildJammed})
	now := schedAt(schedFloor / 2)
	s, woke := s.Observe(now, DemandProbed{Kind: schedWork, Ready: 3, Claims: 0})
	if woke || s.View(schedWork, now).JamUntil.IsZero() {
		t.Fatalf("a probe that raced a claim lifted the jam (woke=%v)", woke)
	}
}

// The child swaps the label before the daemon folds its Claimed, so a probe
// can read the post-swap count first; the fold's decrement then counts that
// claim twice and would make the same count look like a rise.
func TestScheduleDemandSteadyAfterPostSwapProbeAndClaimDoesNotLift(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0,
		DemandProbed{Kind: schedWork, Ready: 2, Claims: 0}, // post-swap count, pre-fold
		Claimed{Kind: schedWork},
		ChildDone{Kind: schedWork, Result: ChildJammed})
	now := schedAt(schedFloor / 2)
	s, woke := s.Observe(now, DemandProbed{Kind: schedWork, Ready: 2, Claims: 1})
	if woke || s.View(schedWork, now).JamUntil.IsZero() {
		t.Fatalf("steady count lifted the jam (woke=%v)", woke)
	}
	if v := s.View(schedWork, now); v.JamBaselinePending || v.ReadyAtJam != 2 {
		t.Fatalf("view = %+v, want baseline 2 taken from the probe", v)
	}
	s, woke = s.Observe(now, DemandProbed{Kind: schedWork, Ready: 3, Claims: 1})
	if !woke || !s.View(schedWork, now).JamUntil.IsZero() {
		t.Fatalf("a rise past the deferred baseline kept the jam (woke=%v)", woke)
	}
}

// A failed probe keeps the claim-decremented count, so it is no confirmation.
func TestScheduleDemandSteadyAfterFailedProbeAndClaimDoesNotLift(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0,
		DemandProbed{Kind: schedWork, Ready: 2, Claims: 0},
		Claimed{Kind: schedWork},
		DemandFailed{Kind: schedWork},
		ChildDone{Kind: schedWork, Result: ChildJammed})
	now := schedAt(schedFloor / 2)
	if v := s.View(schedWork, now); !v.JamBaselinePending {
		t.Fatalf("view = %+v, want the baseline pending", v)
	}
	s, woke := s.Observe(now, DemandProbed{Kind: schedWork, Ready: 2, Claims: 1})
	if woke || s.View(schedWork, now).JamUntil.IsZero() {
		t.Fatalf("steady count lifted the jam (woke=%v)", woke)
	}
}

// A probe confirming the count after the claim leaves nothing to defer.
func TestScheduleJamAfterConfirmedClaimTakesBaselineAtOnce(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0,
		DemandProbed{Kind: schedWork, Ready: 3, Claims: 0},
		Claimed{Kind: schedWork},
		DemandProbed{Kind: schedWork, Ready: 2, Claims: 1},
		ChildDone{Kind: schedWork, Result: ChildJammed})
	if v := s.View(schedWork, schedT0); v.JamBaselinePending || v.ReadyAtJam != 2 {
		t.Fatalf("view = %+v, want baseline 2 at once", v)
	}
}

// An empty child zeroes the count with no tracker observation, so a jam after
// it must not record that zero as the baseline for the next probe to beat.
func TestScheduleJamAfterChildEmptyDefersBaseline(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0,
		DemandProbed{Kind: schedWork, Ready: 2, Claims: 0},
		ChildDone{Kind: schedWork, Result: ChildEmpty},
		ChildDone{Kind: schedWork, Result: ChildJammed})
	now := schedAt(schedFloor / 2)
	if v := s.View(schedWork, now); !v.JamBaselinePending {
		t.Fatalf("view = %+v, want the baseline pending", v)
	}
	s, woke := s.Observe(now, DemandProbed{Kind: schedWork, Ready: 2, Claims: 0})
	v := s.View(schedWork, now)
	if woke || v.JamUntil.IsZero() {
		t.Fatalf("steady count lifted the jam (woke=%v)", woke)
	}
	if v.ReadyAtJam != 2 || v.JamBaselinePending {
		t.Fatalf("view = %+v, want baseline 2 settled", v)
	}
}

// The next jam re-records the count, so a rise is judged against the latest jam.
func TestScheduleDemandRiseComparesAgainstLatestJam(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0,
		DemandProbed{Kind: schedWork, Ready: 2},
		ChildDone{Kind: schedWork, Result: ChildJammed})
	now := schedAt(schedFloor / 2)
	s = schedObserve(t, s, now, DemandProbed{Kind: schedWork, Ready: 3})
	s = schedObserve(t, s, now, ChildDone{Kind: schedWork, Result: ChildJammed})
	s = schedObserve(t, s, now, DemandProbed{Kind: schedWork, Ready: 3})
	if !s.View(schedWork, now).Jammed {
		t.Fatal("a probe at the second jam's count lifted it")
	}
}

func TestScheduleProbedEmptyDoesNotGrow(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	now := schedT0
	for i := 0; i < 4; i++ {
		s = schedObserve(t, s, now, DemandProbed{Kind: schedWork, Ready: 1}, ChildDone{Kind: schedWork, Result: ChildEmpty})
		p := s.Decide(now, Occupancy{}).(Park)
		if p.Until.Sub(now) != schedInterval || p.TipPoll {
			t.Fatalf("empty %d: Park %#v, want exactly one interval", i, p)
		}
		now = p.Until
		if d := s.Decide(now, Occupancy{}); !reflect.DeepEqual(d, Probe{Kinds: []Kind{schedWork}}) {
			t.Fatalf("empty %d at the deadline: %#v, want Probe", i, d)
		}
	}
}

// A child's exit 2 zeroes the scheduling count but not the last probed one,
// which only a probe moves: the daemon's zero-crossing events read it.
func TestScheduleEmptyChildKeepsProbedCount(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 3}, ChildDone{Kind: schedWork, Result: ChildEmpty})
	if v := s.View(schedWork, schedT0); v.Ready != 0 || v.Counted != 3 {
		t.Fatalf("after exit 2: Ready=%d Counted=%d, want 0 and 3", v.Ready, v.Counted)
	}
	s = schedObserve(t, s, schedAt(time.Second), DemandProbed{Kind: schedWork, Ready: 0})
	if v := s.View(schedWork, schedAt(time.Second)); v.Counted != 0 {
		t.Fatalf("after probe: Counted=%d, want 0", v.Counted)
	}
}

func TestScheduleContinueMakesDemandStale(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 5},
		ChildDone{Kind: schedWork, Result: ChildJammed}, ChildDone{Kind: schedWork, Result: ChildContinue})
	if d := s.Decide(schedT0, Occupancy{}); !reflect.DeepEqual(d, Probe{Kinds: []Kind{schedWork}}) {
		t.Fatalf("after Continue: %#v, want Probe", d)
	}
	// Continue also reset the jam backoff to the floor.
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 5}, ChildDone{Kind: schedWork, Result: ChildJammed})
	if p := s.Decide(schedT0, Occupancy{}).(Park); p.Until != schedAt(schedFloor) {
		t.Fatalf("jam after Continue parks until %v, want floor", p.Until)
	}
}

// A claim spends the count a child was started against: the kind is re-probed
// before the next start, so the budget is never judged on a count the claim
// has already moved.
func TestScheduleClaimedMakesDemandStale(t *testing.T) {
	s := probedSchedule([]Kind{schedWork, schedButler}, 0)
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 5})
	if d := s.Decide(schedT0, Occupancy{}); d != (Start{Kind: schedWork}) {
		t.Fatalf("before Claimed: %#v, want Start", d)
	}
	s = schedObserve(t, s, schedT0, Claimed{Kind: schedWork})
	if d := s.Decide(schedT0, Occupancy{}); !reflect.DeepEqual(d, Probe{Kinds: []Kind{schedWork}}) {
		t.Fatalf("after Claimed: %#v, want Probe", d)
	}
	if v := s.View(schedWork, schedT0); v.Ready != 4 || v.Counted != 5 {
		t.Fatalf("Claimed must lower Ready by one and leave Counted: %+v", v)
	}
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 1, Claims: 1})
	if d := s.Decide(schedT0, Occupancy{}); d != (Start{Kind: schedWork}) {
		t.Fatalf("after re-probe: %#v, want Start", d)
	}
}

// A probe that began before a claim can land after it; its count still
// includes the claimed item, so it must not re-validate the kind.
func TestScheduleDemandProbedAcrossClaimStaysStale(t *testing.T) {
	s := probedSchedule([]Kind{schedWork, schedButler}, 0)
	s = schedObserve(t, s, schedT0, Claimed{Kind: schedWork})
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 2, Claims: 0})
	if d := s.Decide(schedT0, Occupancy{}); !reflect.DeepEqual(d, Probe{Kinds: []Kind{schedWork}}) {
		t.Fatalf("probe begun before the claim: %#v, want Probe", d)
	}
	if v := s.View(schedWork, schedT0); v.Ready != 2 || v.Counted != 2 {
		t.Fatalf("stale probe's count not recorded: %+v", v)
	}
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 1, Claims: 1})
	if d := s.Decide(schedT0, Occupancy{}); d != (Start{Kind: schedWork}) {
		t.Fatalf("probe begun after the claim: %#v, want Start", d)
	}
}

func TestScheduleClaimedLeavesUnprobedKindAlone(t *testing.T) {
	s := newSchedule([]Kind{schedButler}, 0, schedFloor, schedCap, nil, nil)
	s = schedObserve(t, s, schedT0, ChildDone{Kind: schedButler, Result: ChildEmpty})
	after := schedObserve(t, s, schedT0, Claimed{Kind: schedButler})
	if !reflect.DeepEqual(after, s) {
		t.Fatal("Claimed changed an unprobed kind")
	}
	if got, _ := s.Observe(schedT0, Claimed{Kind: "nope"}); !reflect.DeepEqual(got, s) {
		t.Fatal("Claimed for an unknown kind changed the schedule")
	}
}

func TestScheduleDemandFailedRestsProbingKeepingCount(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 4})
	at := schedAt(schedInterval)
	s = schedObserve(t, s, at, DemandFailed{Kind: schedWork})
	if d := s.Decide(at, Occupancy{}); d != (Start{Kind: schedWork}) {
		t.Fatalf("after failed probe: %#v, want Start on the old count", d)
	}
	v := s.View(schedWork, at)
	if v.Ready != 4 || !v.ProbedAt.Equal(at) {
		t.Fatalf("view = %+v, want ready 4 probedAt %v", v, at)
	}

	s = probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0, DemandFailed{Kind: schedWork})
	if p := s.Decide(schedT0, Occupancy{}).(Park); p.Until != schedAt(schedInterval) {
		t.Fatalf("never-succeeded failed probe parks until %v, want one interval", p.Until)
	}
}

func TestScheduleObserveWokeOnlyWhenStartableSetGrows(t *testing.T) {
	work := func(r ChildOutcome) ChildDone { return ChildDone{Kind: schedWork, Result: r} }
	probe := func(k Kind, n int) DemandProbed { return DemandProbed{Kind: k, Ready: n} }
	probed := func(evs ...SchedEvent) func() Schedule {
		return func() Schedule { return schedObserve(t, probedSchedule(allKinds(), 0), schedT0, evs...) }
	}
	tests := []struct {
		name  string
		build func() Schedule
		at    time.Duration
		ev    SchedEvent
		want  bool
	}{
		{"never-probed kind gains ready", probed(), 0, probe(schedWork, 1), true},
		{"empty kind gains ready", probed(probe(schedWork, 0)), 0, probe(schedWork, 3), true},
		{"stale count refreshed", probed(probe(schedWork, 3)), schedInterval, probe(schedWork, 3), true},
		{"failed probe revives a stale count", probed(probe(schedWork, 3)), schedInterval, DemandFailed{Kind: schedWork}, true},
		{"already startable kind gains more", probed(probe(schedWork, 1)), 0, probe(schedWork, 5), false},
		{"startable kind drains to zero", probed(probe(schedWork, 1)), 0, probe(schedWork, 0), false},
		{"empty kind stays empty", probed(probe(schedWork, 0)), 0, probe(schedWork, 0), false},
		{"failed probe with no count", probed(), 0, DemandFailed{Kind: schedWork}, false},
		{"startable kind child empty", probed(probe(schedWork, 2)), 0, work(ChildEmpty), false},
		{"startable kind child jammed", probed(probe(schedWork, 2)), 0, work(ChildJammed), false},
		{"startable kind child continues", probed(probe(schedWork, 2)), 0, work(ChildContinue), false},
		{"jam-gated kind probed at the jam's count", probed(probe(schedWork, 2), work(ChildJammed)), 0, probe(schedWork, 2), false},
		{"jam-gated kind probed below the jam's count", probed(probe(schedWork, 2), work(ChildJammed)), 0, probe(schedWork, 1), false},
		{"jam-gated kind probed above the jam's count", probed(probe(schedWork, 2), work(ChildJammed)), 0, probe(schedWork, 9), true},
		{"unstartable kind finishes empty", probed(), 0, work(ChildEmpty), false},
		{"exit-driven kind's backoff reset by continue",
			probed(ChildDone{Kind: schedButler, Result: ChildEmpty}), 0, ChildDone{Kind: schedButler, Result: ChildContinue}, true},
		{"exit-driven kind backs off", probed(), 0, ChildDone{Kind: schedButler, Result: ChildEmpty}, false},
		{"unconfigured kind", func() Schedule { return probedSchedule([]Kind{schedWork}, 0) }, 0, probe(schedResearch, 4), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, woke := tc.build().Observe(schedAt(tc.at), tc.ev); woke != tc.want {
				t.Fatalf("woke = %v, want %v", woke, tc.want)
			}
		})
	}
}

func TestScheduleObserveKeepsValueSemantics(t *testing.T) {
	old := newSchedule([]Kind{schedWork}, 0, schedFloor, schedCap, nil, nil)
	_ = schedObserve(t, old, schedT0, ChildDone{Kind: schedWork, Result: ChildEmpty})
	if d := old.Decide(schedT0, Occupancy{}); d != (Start{Kind: schedWork}) {
		t.Fatalf("original schedule mutated by Observe: %#v", d)
	}
	if s, _ := old.Observe(schedT0, ChildDone{Kind: "nope", Result: ChildEmpty}); !reflect.DeepEqual(s, old) {
		t.Fatal("event for an unknown kind changed the schedule")
	}
}

func TestScheduleView(t *testing.T) {
	s := probedSchedule(allKinds(), 0)
	if v := s.View(schedWork, schedT0); v.Gated || !v.Until.IsZero() || !v.Probed || !v.NextProbe.IsZero() {
		t.Fatalf("never probed: %+v", v)
	}

	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 0})
	v := s.View(schedWork, schedAt(time.Second))
	if !v.Gated || !v.Until.Equal(schedAt(schedInterval)) || !v.NextProbe.Equal(schedAt(schedInterval)) || v.Ready != 0 {
		t.Fatalf("fresh empty: %+v", v)
	}
	if v = s.View(schedWork, schedAt(schedInterval)); v.Gated || !v.Until.IsZero() {
		t.Fatalf("elapsed gate must not publish a stale deadline: %+v", v)
	}

	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 7}, ChildDone{Kind: schedWork, Result: ChildJammed})
	v = s.View(schedWork, schedT0)
	if !v.Gated || !v.Jammed || v.ReadyAtJam != 7 || !v.JamUntil.Equal(schedAt(schedFloor)) || !v.Until.Equal(schedAt(schedFloor)) {
		t.Fatalf("jammed: %+v", v)
	}
	if v = s.View(schedWork, schedAt(schedFloor)); v.Gated || !v.JamUntil.IsZero() || !v.Jammed {
		t.Fatalf("jam elapsed: %+v", v)
	}

	s = schedObserve(t, s, schedT0, ChildDone{Kind: schedButler, Result: ChildEmpty})
	if v = s.View(schedButler, schedT0); !v.Gated || v.Probed || !v.Until.Equal(schedAt(schedFloor)) {
		t.Fatalf("unprobed gated: %+v", v)
	}
	if v = s.View(schedButler, schedAt(schedFloor)); v.Gated || !v.Until.IsZero() {
		t.Fatalf("unprobed elapsed: %+v", v)
	}
	if v = s.View("nope", schedT0); !reflect.DeepEqual(v, KindView{}) {
		t.Fatalf("unknown kind: %+v", v)
	}
}

// A failed re-probe after a claim rests on the count the claim already
// lowered, not the pre-claim one, so it cannot re-open a spent budget.
func TestScheduleDemandFailedAfterClaimDoesNotRestartSpentKind(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 1})
	s = schedObserve(t, s, schedT0, Claimed{Kind: schedWork})
	s = schedObserve(t, s, schedT0, DemandFailed{Kind: schedWork})
	if d := s.Decide(schedT0, Occupancy{}); d == (Start{Kind: schedWork}) {
		t.Fatalf("after claim and failed re-probe: %#v, want no Start", d)
	}
	if v := s.View(schedWork, schedT0); v.Ready != 0 || v.Counted != 1 {
		t.Fatalf("Ready/Counted = %d/%d, want 0/1", v.Ready, v.Counted)
	}
}

func TestScheduleTipMovedWokeOnlyWhenALiftedKindBecomesStartable(t *testing.T) {
	work := func(r ChildOutcome) ChildDone { return ChildDone{Kind: schedWork, Result: r} }
	probe := func(n int) DemandProbed { return DemandProbed{Kind: schedWork, Ready: n} }
	tests := []struct {
		name string
		evs  []SchedEvent
		want bool
	}{
		{"jammed kind with ready work", []SchedEvent{probe(3), work(ChildJammed)}, true},
		{"jammed kind with nothing ready", []SchedEvent{probe(0), work(ChildJammed)}, false},
		{"nothing jammed", []SchedEvent{probe(3)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := schedObserve(t, probedSchedule(allKinds(), 0), schedT0, tc.evs...)
			if _, _, woke := s.TipMoved(schedT0); woke != tc.want {
				t.Fatalf("woke = %v, want %v", woke, tc.want)
			}
		})
	}
}

// A child's exit 2 after Demand counted work makes the count suspect, so the
// next probe must skip any adapter cache; a probe that counted none gives no
// reason to distrust it.
func TestScheduleEmptyChildAfterDemandForcesFreshProbe(t *testing.T) {
	probed := DemandProbed{Kind: schedWork, Ready: 3}
	empty := ChildDone{Kind: schedWork, Result: ChildEmpty}

	s := probedSchedule([]Kind{schedWork}, 0)
	if s.View(schedWork, schedT0).Fresh {
		t.Fatal("a new schedule is Fresh")
	}
	s = schedObserve(t, s, schedT0, probed)
	if s.View(schedWork, schedT0).Fresh {
		t.Fatal("Fresh after a probe alone")
	}
	s = schedObserve(t, s, schedT0, empty)
	if !s.View(schedWork, schedT0).Fresh {
		t.Fatal("not Fresh after Counted>0 then exit 2")
	}

	s = schedObserve(t, s, schedAt(time.Second), DemandFailed{Kind: schedWork})
	if !s.View(schedWork, schedAt(time.Second)).Fresh {
		t.Fatal("a failed probe cleared Fresh")
	}
	s = schedObserve(t, s, schedAt(time.Second), DemandProbed{Kind: schedWork, Ready: 3})
	if !s.View(schedWork, schedAt(time.Second)).Fresh {
		t.Fatal("a conditional probe cleared Fresh")
	}
	s = schedObserve(t, s, schedAt(time.Second), DemandProbed{Kind: schedWork, Ready: 3, Fresh: true})
	if s.View(schedWork, schedAt(time.Second)).Fresh {
		t.Fatal("a fresh probe left Fresh set")
	}

	s = probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 0}, empty)
	if s.View(schedWork, schedT0).Fresh {
		t.Fatal("Fresh after exit 2 with Counted==0")
	}
}

// githubSchedule puts work and research on one tracker; the butler stays its own.
func githubSchedule() Schedule {
	return newSchedule(allKinds(), 0, schedFloor, schedCap, map[Kind]time.Duration{
		schedWork: schedInterval, schedResearch: schedInterval,
	}, map[Kind]string{schedWork: "github", schedResearch: "github"})
}

func TestScheduleRateLimitPausesSharedTracker(t *testing.T) {
	reset := schedAt(3 * schedInterval)
	ready := func() Schedule {
		return schedObserve(t, githubSchedule(), schedT0,
			DemandProbed{Kind: schedWork, Ready: 2}, DemandProbed{Kind: schedResearch, Ready: 2})
	}
	s, woke := ready().Observe(schedAt(time.Second), DemandRateLimited{Kind: schedWork, Reset: reset})
	if woke {
		t.Fatal("a rate limit woke the pool")
	}
	// Work and research are both paused: neither starts, probes, or is stale-listed.
	for _, now := range []time.Duration{schedInterval, 2 * schedInterval} {
		got := s.Decide(schedAt(now), Occupancy{})
		if want := (Start{Kind: schedButler}); got != want {
			// the butler is exit-driven and not on the tracker, so it may start.
			t.Fatalf("at %s decide = %#v, want %#v", now, got, want)
		}
	}
	for _, k := range []Kind{schedWork, schedResearch} {
		v := s.View(k, schedAt(2*schedInterval))
		if !v.Gated || v.Until != reset || v.RateLimitedUntil != reset || v.Tracker != "github" {
			t.Fatalf("view(%s) = %#v", k, v)
		}
		if v.Ready != 2 || v.Counted != 2 {
			t.Fatalf("view(%s) lost its count: %#v", k, v)
		}
	}
	if v := s.View(schedButler, schedAt(2*schedInterval)); v.RateLimitedUntil != (time.Time{}) || v.Tracker != string(schedButler) {
		t.Fatalf("butler view = %#v", v)
	}
	// The pause lapsing leaves the count stale, so both kinds re-probe.
	got := s.Decide(reset, Occupancy{})
	if want := (Probe{Kinds: []Kind{schedWork, schedResearch}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("after the pause decide = %#v, want %#v", got, want)
	}
	if v := s.View(schedWork, reset); v.Gated || v.RateLimitedUntil != (time.Time{}) {
		t.Fatalf("view after the pause = %#v", v)
	}
}

func TestScheduleRateLimitParksUntilThePauseEnds(t *testing.T) {
	reset := schedAt(3 * schedInterval)
	s := newSchedule([]Kind{schedWork, schedResearch}, 0, schedFloor, schedCap,
		map[Kind]time.Duration{schedWork: schedInterval, schedResearch: schedInterval},
		map[Kind]string{schedWork: "github", schedResearch: "github"})
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 2}, DemandProbed{Kind: schedResearch, Ready: 2})
	s = schedObserve(t, s, schedAt(time.Second), DemandRateLimited{Kind: schedWork, Reset: reset})
	if got, want := s.Decide(schedAt(2*schedInterval), Occupancy{}), (Park{Until: reset}); got != want {
		t.Fatalf("decide = %#v, want %#v", got, want)
	}
}

func TestScheduleRateLimitWithoutUsableResetPausesFourIntervals(t *testing.T) {
	now := schedAt(time.Second)
	for name, reset := range map[string]time.Time{
		"no reset":      {},
		"reset in past": schedT0,
		"reset is now":  now,
	} {
		t.Run(name, func(t *testing.T) {
			s := schedObserve(t, githubSchedule(), now, DemandRateLimited{Kind: schedResearch, Reset: reset})
			want := now.Add(4 * schedInterval)
			for _, k := range []Kind{schedWork, schedResearch} {
				if v := s.View(k, now); v.RateLimitedUntil != want {
					t.Fatalf("view(%s).RateLimitedUntil = %v, want %v", k, v.RateLimitedUntil, want)
				}
			}
		})
	}
}

func reportedSchedule(kinds ...Kind) Schedule {
	return newSchedule(kinds, 0, schedFloor, schedCap, nil, nil)
}

func TestScheduleReportedKind(t *testing.T) {
	due := schedAt(time.Hour)
	empty := func(nd NextDue) SchedEvent {
		return ChildDone{Kind: schedButler, Result: ChildEmpty, NextDue: nd}
	}
	tests := []struct {
		name  string
		kinds []Kind
		evs   []SchedEvent
		now   time.Duration
		want  Decision
	}{
		{"starts at start-up to learn next_due", []Kind{schedButler}, nil, 0,
			Start{Kind: schedButler}},
		{"empty with an instant parks until it", []Kind{schedButler},
			[]SchedEvent{empty(NextDue{At: due})}, 0,
			Park{Until: due}},
		{"empty with an instant starts at it", []Kind{schedButler},
			[]SchedEvent{empty(NextDue{At: due})}, time.Hour,
			Start{Kind: schedButler}},
		{"empty on tip move only parks one cap out and polls", []Kind{schedButler},
			[]SchedEvent{empty(NextDue{OnTipMove: true})}, 0,
			Park{Until: schedAt(schedCap), TipPoll: true}},
		{"tip-move only never starts by time", []Kind{schedButler},
			[]SchedEvent{empty(NextDue{OnTipMove: true})}, 24 * time.Hour,
			Park{Until: schedAt(24*time.Hour + schedCap), TipPoll: true}},
		{"instant and tip move parks until the instant and polls", []Kind{schedButler},
			[]SchedEvent{empty(NextDue{At: due, OnTipMove: true})}, 0,
			Park{Until: due, TipPoll: true}},
		{"empty with no report falls back to the backoff", []Kind{schedButler},
			[]SchedEvent{empty(NextDue{})}, 0,
			Park{Until: schedAt(schedFloor)}},
		{"continue is startable", []Kind{schedButler},
			[]SchedEvent{empty(NextDue{At: due}), ChildDone{Kind: schedButler, Result: ChildContinue}}, 0,
			Start{Kind: schedButler}},
		{"jammed backs off with a poll", []Kind{schedButler},
			[]SchedEvent{ChildDone{Kind: schedButler, Result: ChildJammed}}, 0,
			Park{Until: schedAt(schedFloor), TipPoll: true}},
		{"a waiting reported kind does not block a normal kind", []Kind{schedButler, schedWork},
			[]SchedEvent{empty(NextDue{At: due})}, 0,
			Start{Kind: schedWork}},
		{"a waiting reported kind leaves the sooner sibling deadline", []Kind{schedButler, schedWork},
			[]SchedEvent{empty(NextDue{At: due}), ChildDone{Kind: schedWork, Result: ChildEmpty}}, 0,
			Park{Until: schedAt(schedFloor)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := schedObserve(t, reportedSchedule(tt.kinds...), schedT0, tt.evs...)
			if got := s.Decide(schedAt(tt.now), Occupancy{}); got != tt.want {
				t.Fatalf("Decide = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestScheduleRateLimitOnUntrackedKindStaysAlone(t *testing.T) {
	s := schedObserve(t, githubSchedule(), schedT0, DemandRateLimited{Kind: schedButler})
	for _, k := range []Kind{schedWork, schedResearch} {
		if v := s.View(k, schedT0); v.RateLimitedUntil != (time.Time{}) {
			t.Fatalf("view(%s) paused by another tracker: %#v", k, v)
		}
	}
}

func TestScheduleRateLimitPauseOutlastsJamGate(t *testing.T) {
	s := schedObserve(t, githubSchedule(), schedT0,
		DemandProbed{Kind: schedWork, Ready: 1},
		ChildDone{Kind: schedWork, Result: ChildJammed},
		DemandRateLimited{Kind: schedWork, Reset: schedAt(time.Minute)})
	if v := s.View(schedWork, schedT0); !v.Gated || v.Until != schedAt(time.Minute) || !v.Jammed {
		t.Fatalf("view = %#v", v)
	}
}

func TestScheduleRateLimitKeepsTheLaterPause(t *testing.T) {
	now := schedAt(time.Second)
	reset := schedAt(time.Hour)
	s := schedObserve(t, githubSchedule(), now,
		DemandRateLimited{Kind: schedWork, Reset: reset},
		// A sibling probe already in flight when the tracker paused.
		DemandRateLimited{Kind: schedResearch})
	for _, k := range []Kind{schedWork, schedResearch} {
		if v := s.View(k, now); v.RateLimitedUntil != reset {
			t.Fatalf("view(%s).RateLimitedUntil = %v, want %v", k, v.RateLimitedUntil, reset)
		}
	}
}

func TestScheduleReportedTipMoveLiftsWaitingKind(t *testing.T) {
	s := schedObserve(t, reportedSchedule(schedWork, schedButler), schedT0,
		ChildDone{Kind: schedWork, Result: ChildEmpty},
		ChildDone{Kind: schedButler, Result: ChildEmpty, NextDue: NextDue{OnTipMove: true}})
	s, lifted, woke := s.TipMoved(schedT0)
	if want := []Kind{schedButler}; !reflect.DeepEqual(lifted, want) || !woke {
		t.Fatalf("lifted = %v woke = %v, want %v and woke", lifted, woke, want)
	}
	if d := s.Decide(schedT0, Occupancy{}); d != (Start{Kind: schedButler}) {
		t.Fatalf("after lift: %#v, want Start butler", d)
	}
	if _, lifted, _ = s.TipMoved(schedT0); len(lifted) != 0 {
		t.Fatalf("second lift = %v, want none", lifted)
	}
}

func TestScheduleReportedTipMoveDoesNotLiftTimedWait(t *testing.T) {
	s := schedObserve(t, reportedSchedule(schedButler), schedT0,
		ChildDone{Kind: schedButler, Result: ChildEmpty, NextDue: NextDue{At: schedAt(time.Hour)}})
	if _, lifted, woke := s.TipMoved(schedT0); len(lifted) != 0 || woke {
		t.Fatalf("lifted = %v woke = %v, want neither", lifted, woke)
	}
}

func TestNextDueMerge(t *testing.T) {
	a, b := schedAt(time.Hour), schedAt(2*time.Hour)
	tests := []struct {
		name       string
		n, o, want NextDue
	}{
		{"zero folds to the record", NextDue{}, NextDue{At: b}, NextDue{At: b}},
		{"earliest instant wins", NextDue{At: b}, NextDue{At: a}, NextDue{At: a}},
		{"later instant is ignored", NextDue{At: a}, NextDue{At: b}, NextDue{At: a}},
		{"tip move ORs in", NextDue{At: a}, NextDue{OnTipMove: true}, NextDue{At: a, OnTipMove: true}},
		{"tip move is kept", NextDue{OnTipMove: true}, NextDue{At: a}, NextDue{At: a, OnTipMove: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.n.Merge(tt.o); got != tt.want {
				t.Fatalf("Merge = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestScheduleReportedView(t *testing.T) {
	due := schedAt(time.Hour)
	s := reportedSchedule(schedWork, schedButler)
	if v := s.View(schedWork, schedT0); !v.NextDue.IsZero() || v.Gated {
		t.Fatalf("work view = %+v, want no due state, ungated", v)
	}
	if v := s.View(schedButler, schedT0); !v.NextDue.IsZero() || v.Gated {
		t.Fatalf("start-up butler view = %+v, want unknown, ungated", v)
	}

	timed := schedObserve(t, s, schedT0,
		ChildDone{Kind: schedButler, Result: ChildEmpty, NextDue: NextDue{At: due}})
	v := timed.View(schedButler, schedT0)
	if v.NextDue.At != due || !v.Gated || v.Until != due {
		t.Fatalf("timed view = %+v, want gated until %v", v, due)
	}
	if v := timed.View(schedButler, due); v.Gated || !v.Until.IsZero() {
		t.Fatalf("view at due = %+v, want ungated with zero Until", v)
	}

	tip := schedObserve(t, s, schedT0,
		ChildDone{Kind: schedButler, Result: ChildEmpty, NextDue: NextDue{OnTipMove: true}})
	if v := tip.View(schedButler, schedT0); !v.Gated || !v.Until.IsZero() || !v.NextDue.OnTipMove {
		t.Fatalf("tip-only view = %+v, want gated with zero Until", v)
	}

	backoff := schedObserve(t, s, schedT0, ChildDone{Kind: schedButler, Result: ChildEmpty})
	if v := backoff.View(schedButler, schedT0); !v.NextDue.IsZero() || !v.Gated || !v.Until.Equal(schedAt(schedFloor)) {
		t.Fatalf("fallback view = %+v, want the backoff gate", v)
	}
}

// burst feeds n no-work results of res to kind, all from children started
// under gate, a few seconds apart from at.
func burst(t *testing.T, s Schedule, at time.Time, kind Kind, res ChildOutcome, gateGen, n int) Schedule {
	t.Helper()
	for i := 0; i < n; i++ {
		s = schedObserve(t, s, at.Add(time.Duration(i)*time.Second), ChildDone{Kind: kind, Result: res, GateGen: gateGen})
	}
	return s
}

func TestScheduleUnprobedBurstRaisesBackoffOnce(t *testing.T) {
	const floor, ceil = 5 * time.Minute, 30 * time.Minute
	newS := func(kind Kind) Schedule { return newSchedule([]Kind{kind}, 0, floor, ceil, nil, nil) }

	t.Run("burst from ungated start gates at floor", func(t *testing.T) {
		s := burst(t, newS(schedResearch), schedT0, schedResearch, ChildEmpty, 0, 4)
		if v := s.View(schedResearch, schedT0); !v.Gated || v.Until != schedT0.Add(floor) {
			t.Fatalf("View = %+v, want gated until floor", v)
		}
	})

	for _, kind := range []Kind{schedResearch, schedButler} {
		t.Run("burst under a lapsed gate steps once "+string(kind), func(t *testing.T) {
			s := burst(t, newS(kind), schedT0, kind, ChildEmpty, 0, 1)
			end := schedT0.Add(floor)
			s = burst(t, s, end, kind, ChildEmpty, s.gateGenOf(kind), 4)
			if v := s.View(kind, end); v.Until != end.Add(2*floor) {
				t.Fatalf("Until = %v, want first result + %v", v.Until.Sub(end), 2*floor)
			}
		})
	}

	t.Run("distinct windows still double to cap", func(t *testing.T) {
		s := newS(schedResearch)
		now := schedT0
		var waits []time.Duration
		for i := 0; i < 5; i++ {
			s = schedObserve(t, s, now, ChildDone{Kind: schedResearch, Result: ChildEmpty, GateGen: s.gateGenOf(schedResearch)})
			v := s.View(schedResearch, now)
			waits = append(waits, v.Until.Sub(now))
			now = v.Until
		}
		want := []time.Duration{floor, 2 * floor, 4 * floor, ceil, ceil}
		if !reflect.DeepEqual(waits, want) {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	})

	t.Run("jammed burst is one step and tip-liftable", func(t *testing.T) {
		s := burst(t, newS(schedWork), schedT0, schedWork, ChildJammed, 0, 4)
		if v := s.View(schedWork, schedT0); !v.Jammed || v.Until != schedT0.Add(floor) {
			t.Fatalf("View = %+v, want jammed at floor", v)
		}
		s, lifted, _ := s.TipMoved(schedT0)
		if !reflect.DeepEqual(lifted, []Kind{schedWork}) {
			t.Fatalf("lifted = %v", lifted)
		}
		if d := s.Decide(schedT0, Occupancy{}); d != (Start{Kind: schedWork}) {
			t.Fatalf("after lift: %#v, want Start", d)
		}
	})

	t.Run("empty then jammed in one burst is jammed", func(t *testing.T) {
		s := burst(t, newS(schedWork), schedT0, schedWork, ChildEmpty, 0, 1)
		s = burst(t, s, schedT0, schedWork, ChildJammed, 0, 1)
		if v := s.View(schedWork, schedT0); !v.Jammed || v.Until != schedT0.Add(floor) {
			t.Fatalf("View = %+v, want jammed at floor", v)
		}
	})

	t.Run("stale-gen empty result after the gate lapsed marks afresh", func(t *testing.T) {
		s := burst(t, newS(schedResearch), schedT0, schedResearch, ChildEmpty, 0, 1)
		end := schedT0.Add(floor)
		s = burst(t, s, end, schedResearch, ChildEmpty, 0, 1)
		if v := s.View(schedResearch, end); !v.Gated || v.Until != end.Add(2*floor) {
			t.Fatalf("View = %+v, want gated until %v", v, 2*floor)
		}
	})

	t.Run("stale-gen jammed result after the gate lapsed re-gates the kind", func(t *testing.T) {
		s := burst(t, newS(schedWork), schedT0, schedWork, ChildEmpty, 0, 1)
		end := schedT0.Add(floor)
		s = burst(t, s, end, schedWork, ChildJammed, 0, 1)
		if v := s.View(schedWork, end); !v.Gated || !v.Jammed || v.Until != end.Add(2*floor) {
			t.Fatalf("View = %+v, want jammed and gated until %v", v, 2*floor)
		}
	})

	t.Run("stale gate after a Continue reset still marks", func(t *testing.T) {
		s := newS(schedResearch)
		s = burst(t, s, schedT0, schedResearch, ChildEmpty, 0, 1)
		s = schedObserve(t, s, schedT0, ChildDone{Kind: schedResearch, Result: ChildContinue})
		s = burst(t, s, schedT0, schedResearch, ChildEmpty, 0, 1)
		if v := s.View(schedResearch, schedT0); v.Until != schedT0.Add(floor) {
			t.Fatalf("Until = %v, want floor", v.Until.Sub(schedT0))
		}
	})
}

func TestScheduleProbedEmptyBurstStillResets(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 5})
	s = burst(t, s, schedT0, schedWork, ChildEmpty, 0, 4)
	if v := s.View(schedWork, schedT0); v.Jammed {
		t.Fatalf("View = %+v, want not jammed", v)
	}
	if got := s.gateGenOf(schedWork); got != 0 {
		t.Fatalf("probed empty burst set %d gates, want 0", got)
	}
}
