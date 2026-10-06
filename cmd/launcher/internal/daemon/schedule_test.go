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
	})
}

func schedObserve(t *testing.T, s Schedule, now time.Time, evs ...SchedEvent) Schedule {
	t.Helper()
	for _, ev := range evs {
		var woke bool
		s, woke = s.Observe(now, ev)
		if woke {
			t.Fatalf("Observe(%v) woke; this slice never wakes", ev)
		}
	}
	return s
}

func allKinds() []Kind { return []Kind{schedWork, schedResearch, schedButler} }

func TestScheduleDecide(t *testing.T) {
	running := func(research int) Occupancy {
		return Occupancy{Running: map[Kind]int{schedResearch: research}}
	}
	starting := func(kind Kind, n int) Occupancy {
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
			occ:  starting(schedWork, 1),
			now:  3 * time.Second,
			want: Park{Until: schedAt(schedInterval)},
		},
		{
			name: "start budget left: ready 3 starting 2 starts",
			build: func() Schedule {
				return probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 3)
			},
			occ:  starting(schedWork, 2),
			want: Start{Kind: schedWork},
		},
		{
			name: "start budget over-spent against a lower count still does not start",
			build: func() Schedule {
				return probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 1)
			},
			occ:  starting(schedWork, 2),
			want: Park{Until: schedAt(schedInterval)},
		},
		{
			name: "start budget spent: the next kind in priority order starts",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 0), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			occ:  starting(schedWork, 1),
			want: Start{Kind: schedResearch},
		},
		{
			name: "start budget is per kind: starting research leaves work its budget",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 0), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			occ:  starting(schedResearch, 1),
			want: Start{Kind: schedWork},
		},
		{
			name: "start budget spent on reserved kind with floor unmet: work starts",
			build: func() Schedule {
				s := probed(probedSchedule(allKinds(), 2), schedT0, schedWork, 1)
				return probed(s, schedT0, schedResearch, 1)
			},
			occ:  starting(schedResearch, 1),
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
			occ:  starting(schedWork, 1),
			want: Probe{Kinds: []Kind{schedResearch}},
		},
		{
			name: "start budget spent on a stale kind probes it rather than starting",
			build: func() Schedule {
				return probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 1)
			},
			occ:  starting(schedWork, 1),
			now:  schedInterval,
			want: Probe{Kinds: []Kind{schedWork}},
		},
		{
			name: "unprobed kind ignores the start budget",
			build: func() Schedule {
				return newSchedule([]Kind{schedButler}, 0, schedFloor, schedCap, nil)
			},
			occ:  starting(schedButler, 3),
			want: Start{Kind: schedButler},
		},
		{
			name: "jammed kind parks until the jam ends with JamPoll",
			build: func() Schedule {
				s := probed(probedSchedule([]Kind{schedWork}, 0), schedT0, schedWork, 3)
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildJammed})
			},
			now:  500 * time.Millisecond,
			want: Park{Until: schedAt(schedFloor), JamPoll: true},
		},
		{
			name: "jam gate hides a stale kind from probing",
			build: func() Schedule {
				s := newSchedule([]Kind{schedWork}, 0, schedFloor, schedCap, map[Kind]time.Duration{schedWork: 500 * time.Millisecond})
				s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 3})
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildJammed})
			},
			now:  600 * time.Millisecond,
			want: Park{Until: schedAt(schedFloor), JamPoll: true},
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
				return newSchedule([]Kind{schedButler}, 0, schedFloor, schedCap, nil)
			},
			want: Start{Kind: schedButler},
		},
		{
			name: "single unprobed kind: empty exit parks on the backoff",
			build: func() Schedule {
				s := newSchedule([]Kind{schedWork}, 0, schedFloor, schedCap, nil)
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildEmpty})
			},
			want: Park{Until: schedAt(schedFloor)},
		},
		{
			name: "single unprobed kind: jammed exit parks with JamPoll",
			build: func() Schedule {
				s := newSchedule([]Kind{schedWork}, 0, schedFloor, schedCap, nil)
				return schedObserve(t, s, schedT0, ChildDone{Kind: schedWork, Result: ChildJammed})
			},
			want: Park{Until: schedAt(schedFloor), JamPoll: true},
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
	s := newSchedule([]Kind{schedButler}, 0, schedFloor, schedCap, nil)
	now := schedT0
	var waits []time.Duration
	for i := 0; i < 5; i++ {
		// Exit 2 and 3 alike grow the same backoff.
		res := ChildEmpty
		if i%2 == 1 {
			res = ChildJammed
		}
		s = schedObserve(t, s, now, ChildDone{Kind: schedButler, Result: res})
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
	s = schedObserve(t, s, now, ChildDone{Kind: schedButler, Result: ChildEmpty})
	if p := s.Decide(now, Occupancy{}).(Park); p.Until.Sub(now) != schedFloor {
		t.Fatalf("backoff after Continue reset waits %v, want floor", p.Until.Sub(now))
	}
}

func TestScheduleTipMovedResetsOnlyJammedKinds(t *testing.T) {
	s := newSchedule(allKinds(), 0, schedFloor, schedCap, nil)
	s = schedObserve(t, s, schedT0,
		ChildDone{Kind: schedWork, Result: ChildJammed},
		ChildDone{Kind: schedResearch, Result: ChildEmpty},
		ChildDone{Kind: schedButler, Result: ChildJammed})

	s, lifted := s.LiftJams()
	if want := []Kind{schedWork, schedButler}; !reflect.DeepEqual(lifted, want) {
		t.Fatalf("lifted = %v, want %v", lifted, want)
	}
	if d := s.Decide(schedT0, Occupancy{}); d != (Start{Kind: schedWork}) {
		t.Fatalf("after lift: %#v, want Start work", d)
	}
	if v := s.View(schedResearch, schedT0); !v.Gated {
		t.Fatal("queue-empty research must stay gated across a moved tip")
	}
	if _, lifted = s.LiftJams(); len(lifted) != 0 {
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
		if !p.JamPoll {
			t.Fatalf("jam %d: JamPoll false", i)
		}
		waits = append(waits, p.Until.Sub(now))
		now = p.Until
	}
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("jam waits = %v, want %v", waits, want)
	}

	s = schedObserve(t, s, now, DemandProbed{Kind: schedWork, Ready: 5}, ChildDone{Kind: schedWork, Result: ChildJammed})
	s, _ = s.LiftJams()
	if d := s.Decide(now, Occupancy{}); d != (Start{Kind: schedWork}) {
		t.Fatalf("after LiftJams: %#v, want Start", d)
	}
	s = schedObserve(t, s, now, ChildDone{Kind: schedWork, Result: ChildJammed})
	if p := s.Decide(now, Occupancy{}).(Park); p.Until.Sub(now) != schedFloor {
		t.Fatalf("jam after lift waits %v, want floor", p.Until.Sub(now))
	}
}

func TestScheduleProbedEmptyDoesNotGrow(t *testing.T) {
	s := probedSchedule([]Kind{schedWork}, 0)
	now := schedT0
	for i := 0; i < 4; i++ {
		s = schedObserve(t, s, now, DemandProbed{Kind: schedWork, Ready: 1}, ChildDone{Kind: schedWork, Result: ChildEmpty})
		p := s.Decide(now, Occupancy{}).(Park)
		if p.Until.Sub(now) != schedInterval || p.JamPoll {
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
	if v := s.View(schedWork, schedT0); v.Ready != 5 || v.Counted != 5 {
		t.Fatalf("Claimed changed the count: %+v", v)
	}
	s = schedObserve(t, s, schedT0, DemandProbed{Kind: schedWork, Ready: 1})
	if d := s.Decide(schedT0, Occupancy{}); d != (Start{Kind: schedWork}) {
		t.Fatalf("after re-probe: %#v, want Start", d)
	}
}

func TestScheduleClaimedLeavesUnprobedKindAlone(t *testing.T) {
	s := newSchedule([]Kind{schedButler}, 0, schedFloor, schedCap, nil)
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

func TestScheduleObserveKeepsValueSemantics(t *testing.T) {
	old := newSchedule([]Kind{schedWork}, 0, schedFloor, schedCap, nil)
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
