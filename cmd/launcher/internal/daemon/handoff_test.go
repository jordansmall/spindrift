package daemon

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
)

func fullHandoff() Handoff {
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 123456789, time.UTC)
	return Handoff{
		LockFD:     3,
		FromPath:   "/nix/store/aaa-old/bin/daemon",
		ToPath:     "/nix/store/bbb-new/bin/daemon",
		Revision:   "rev1",
		RolledBack: true,
		Reason:     "preflight: doctor exited 1",
		Stop:       true,
		Abort:      true,
		Slots: []HandoffSlot{
			{
				Slot: 0, Kind: "work", Revision: "abc123",
				ChildStart: "2026-10-11T12:00:00Z", ChildStartN: 2,
				ChildLog: ".spindrift/child-0.log", GateGen: 4,
				Key:    dispatchkey.Issue("42"),
				Issues: []string{"42", "43"},
				Pass:   "fix-pass-1", Model: "claude-x", ModelRole: "fixer",
				CIWaitPRURL: "https://github.com/o/r/pull/42", CIWaitRunURL: "https://github.com/o/r/actions/runs/7",
				Child: ChildState{PID: 4242, ReportFD: 7, Partial: []byte("{\"event\":\"caf\xc3"), Discarding: true},
			},
			{
				Slot: 3, Kind: "butler", Revision: "def456",
				Key:   dispatchkey.Chore("bugs"),
				Child: ChildState{PID: 99, ReportFD: 9},
			},
			// No box record yet, so no key.
			{Slot: 4, Kind: "work", Revision: "def456", Child: ChildState{PID: 100, ReportFD: 10}},
		},
		Kinds: []HandoffKind{
			{
				Kind: "work", BackoffCur: 2 * time.Minute, Until: t0.Add(time.Minute),
				Jammed: true, Gen: 5,
				DueAt: t0.Add(time.Hour), DueOnTipMove: true,
				LimitedUntil: t0.Add(30 * time.Second),
			},
			{Kind: "research", BackoffCur: time.Second},
		},
		Breaker: []time.Time{t0, t0.Add(time.Second)},
	}
}

func TestHandoffRoundTrip(t *testing.T) {
	want := fullHandoff()
	var buf bytes.Buffer
	if err := EncodeHandoff(&buf, want); err != nil {
		t.Fatalf("EncodeHandoff: %v", err)
	}
	got, err := DecodeHandoff(&buf)
	if err != nil {
		t.Fatalf("DecodeHandoff: %v", err)
	}
	want.V = HandoffVersion
	if !reflect.DeepEqual(want, got) {
		t.Errorf("round trip mismatch\nwant %+v\n got %+v", want, got)
	}
}

func TestHandoffKeysTravelAsIssueAndChoreFields(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeHandoff(&buf, fullHandoff()); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	for _, frag := range []string{`"issue":"42"`, `"chore":"bugs"`, `"v":3`, `"ci_wait_pr_url":"https://github.com/o/r/pull/42"`} {
		if !strings.Contains(s, frag) {
			t.Errorf("document missing %s: %s", frag, s)
		}
	}
}

func TestHandoffEncodeStampsVersion(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeHandoff(&buf, Handoff{V: 99}); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeHandoff(&buf)
	if err != nil {
		t.Fatalf("DecodeHandoff: %v", err)
	}
	if got.V != HandoffVersion {
		t.Errorf("V = %d, want %d", got.V, HandoffVersion)
	}
}

func TestHandoffDecodeRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		want string
	}{
		"future version":      {`{"v":4,"lock_fd":3,"newthing":[1]}`, "version 4"},
		"missing version":     {`{"lock_fd":3}`, "version 0"},
		"malformed":           {`{"v":1,`, "decode"},
		"unknown field":       {`{"v":1,"lock_fd":3,"bogus":true}`, "bogus"},
		"both key fields":     {`{"v":1,"lock_fd":3,"slots":[{"slot":0,"kind":"work","issue":"1","chore":"x"}]}`, "slot"},
		"trailing data":       {`{"v":1,"lock_fd":3}{"v":1}`, "trailing"},
		"garbage after":       {`{"v":1,"lock_fd":3} }`, "after the document"},
		"negative lock fd":    {`{"v":1,"lock_fd":-1}`, "lock_fd"},
		"zero pid":            {`{"v":1,"lock_fd":3,"slots":[{"slot":2,"kind":"work","child":{"pid":0,"report_fd":5}}]}`, "slot 2"},
		"negative pid":        {`{"v":1,"lock_fd":3,"slots":[{"slot":6,"kind":"work","child":{"pid":-4,"report_fd":5}}]}`, "slot 6"},
		"duplicate pid":       {`{"v":1,"lock_fd":3,"slots":[{"slot":1,"kind":"work","child":{"pid":4,"report_fd":5}},{"slot":2,"kind":"work","child":{"pid":4,"report_fd":6}}]}`, "slot 2"},
		"duplicate report fd": {`{"v":1,"lock_fd":3,"slots":[{"slot":1,"kind":"work","child":{"pid":4,"report_fd":5}},{"slot":2,"kind":"work","child":{"pid":5,"report_fd":5}}]}`, "already slot 1's"},
		"report fd is lock":   {`{"v":1,"lock_fd":3,"slots":[{"slot":1,"kind":"work","child":{"pid":4,"report_fd":3}}]}`, "is the lock_fd"},
		"negative report fd":  {`{"v":1,"lock_fd":3,"slots":[{"slot":1,"kind":"work","child":{"pid":4,"report_fd":-1}}]}`, "slot 1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeHandoff(strings.NewReader(tc.doc))
			if err == nil {
				t.Fatal("DecodeHandoff accepted the document")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// An older predecessor's document, written before the CI-wait fields, still
// resumes; only a rollback onto it is refused.
func TestHandoffDecodeAcceptsOlderVersions(t *testing.T) {
	for n := handoffMinVersion; n < HandoffRollbackVersion; n++ {
		v := strconv.Itoa(n)
		doc := `{"v":` + v + `,"lock_fd":3,"slots":[{"slot":1,"kind":"work","child":{"pid":4,"report_fd":5}}]}`
		got, err := DecodeHandoff(strings.NewReader(doc))
		if err != nil {
			t.Fatalf("v%s: DecodeHandoff: %v", v, err)
		}
		if len(got.Slots) != 1 || got.Slots[0].CIWaitPRURL != "" {
			t.Errorf("v%s: slots = %+v, want one slot with no CI wait", v, got.Slots)
		}
	}
}

func TestHandoffVersionErrorNamesSupported(t *testing.T) {
	_, err := DecodeHandoff(strings.NewReader(`{"v":7}`))
	if err == nil || !strings.Contains(err.Error(), "supports 1 to 3") {
		t.Errorf("error %v should name the supported version", err)
	}
}

// snapshot is restore's inverse: what a predecessor writes is what its
// successor resumes from, and a pristine schedule or breaker writes nothing.
func TestScheduleSnapshotInvertsRestore(t *testing.T) {
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	s := newSchedule([]Kind{workKind, butlerKind}, 0, time.Second, time.Minute, nil, nil)
	if got := s.snapshot(); got != nil {
		t.Fatalf("pristine snapshot = %+v, want none", got)
	}
	want := []HandoffKind{{
		Kind: workKind, BackoffCur: 4 * time.Second, Until: t0, Jammed: true, Gen: 3,
		DueAt: t0.Add(time.Hour), DueOnTipMove: true, LimitedUntil: t0.Add(time.Minute),
	}}

	got := s.restore(want).snapshot()

	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot after restore = %+v, want %+v", got, want)
	}
}

func TestBreakerSnapshotInvertsRestore(t *testing.T) {
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	want := []time.Time{t0, t0.Add(time.Second)}

	got := newBreaker(3, time.Minute).restore(want).snapshot()

	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot after restore = %v, want %v", got, want)
	}
	if got := newBreaker(3, time.Minute).snapshot(); len(got) != 0 {
		t.Errorf("pristine snapshot = %v, want none", got)
	}
}

func TestHandoffDecodeAcceptsPreviousVersion(t *testing.T) {
	h, err := DecodeHandoff(strings.NewReader(`{"v":1,"lock_fd":3,"from_path":"/nix/store/a/bin/d"}`))
	if err != nil {
		t.Fatalf("DecodeHandoff refused a v1 document: %v", err)
	}
	if h.V != 1 || h.FromPath != "/nix/store/a/bin/d" {
		t.Errorf("decoded %+v, want the v1 document as written", h)
	}
}

// A gate carried across a handoff keeps the backoff step its predecessor
// earned: the successor holds no jam baseline, so its first probe takes one
// rather than lifting the gate (and resetting the step) on a flat count. An
// exit from a child the successor inherited can land before that probe, and
// must not take a zero baseline in its place.
func TestScheduleRestoreRetakesJamBaseline(t *testing.T) {
	recoverProbe := DemandProbed{Kind: schedRecover, Ready: 1, IDs: []string{"7"}}
	cases := []struct {
		name        string
		fresh       func() Schedule
		kind        Kind
		predecessor func(*testing.T, Schedule) (Schedule, time.Time)
		exit        ChildDone // an inherited child's exit
		flat        DemandProbed
		rise        DemandProbed
	}{
		{
			name:  "jammed work",
			fresh: func() Schedule { return probedSchedule([]Kind{schedWork}, 0) },
			kind:  schedWork,
			predecessor: func(t *testing.T, s Schedule) (Schedule, time.Time) {
				s = schedObserve(t, s, schedT0,
					DemandProbed{Kind: schedWork, Ready: 3},
					ChildDone{Kind: schedWork, Result: ChildJammed},
					ChildDone{Kind: schedWork, Result: ChildJammed})
				return s, schedT0
			},
			exit: ChildDone{Kind: schedWork, Result: ChildJammed},
			flat: DemandProbed{Kind: schedWork, Ready: 3},
			rise: DemandProbed{Kind: schedWork, Ready: 4},
		},
		{
			name:  "upper-bound recover",
			fresh: upperBoundSchedule,
			kind:  schedRecover,
			predecessor: func(t *testing.T, s Schedule) (Schedule, time.Time) {
				now := schedT0
				s = schedObserve(t, s, now, recoverProbe, ChildDone{Kind: schedRecover, Result: ChildEmpty})
				now = s.View(schedRecover, now).Until
				s = schedObserve(t, s, now, recoverProbe, ChildDone{Kind: schedRecover, Result: ChildEmpty})
				return s, now
			},
			exit: ChildDone{Kind: schedRecover, Result: ChildEmpty},
			flat: recoverProbe,
			rise: DemandProbed{Kind: schedRecover, Ready: 1, IDs: []string{"8"}},
		},
	}
	step := func(s Schedule, kind Kind) time.Duration {
		for _, hk := range s.snapshot() {
			if hk.Kind == kind {
				return hk.BackoffCur
			}
		}
		return 0
	}
	for _, c := range cases {
		for _, inherited := range []bool{false, true} {
			name := c.name
			if inherited {
				name += "/inherited child exits first"
			}
			t.Run(name, func(t *testing.T) {
				s, at := c.predecessor(t, c.fresh())
				pred := s.View(c.kind, at)
				if !pred.Gated || pred.Until.IsZero() {
					t.Fatalf("predecessor view = %+v, want a gated kind", pred)
				}
				carriedStep := step(s, c.kind)
				if carriedStep <= schedFloor {
					t.Fatalf("predecessor step = %v, want it escalated past %v", carriedStep, schedFloor)
				}
				succ := c.fresh().restore(s.snapshot())
				mid := at.Add(pred.Until.Sub(at) / 2)
				if v := succ.View(c.kind, mid); !v.JamBaselinePending {
					t.Fatalf("successor view = %+v, want the baseline pending", v)
				}
				if got := step(succ, c.kind); got != carriedStep {
					t.Fatalf("carried step = %v, want %v", got, carriedStep)
				}

				want := carriedStep
				if inherited {
					succ = schedObserve(t, succ, mid, c.exit)
					want = step(succ, c.kind)
					if want < carriedStep {
						t.Fatalf("inherited exit dropped the step to %v, want at least %v", want, carriedStep)
					}
					if v := succ.View(c.kind, mid); !v.Gated || !v.JamBaselinePending {
						t.Fatalf("after the inherited exit view = %+v, want gated with the baseline pending", v)
					}
				}

				succ = schedObserve(t, succ, mid, c.flat)
				fv := succ.View(c.kind, mid)
				if !fv.Gated || fv.JamBaselinePending {
					t.Fatalf("after a flat probe view = %+v, want gated with the baseline settled", fv)
				}
				if !inherited && fv.Until != pred.Until {
					t.Fatalf("flat probe moved the gate to %v, want %v", fv.Until, pred.Until)
				}
				if got := step(succ, c.kind); got != want {
					t.Fatalf("flat probe moved the step to %v, want %v", got, want)
				}

				succ = schedObserve(t, succ, mid, c.rise)
				if v := succ.View(c.kind, mid); v.Gated {
					t.Fatalf("after a rise view = %+v, want the gate lifted", v)
				}
			})
		}
	}
}

// Only a probed kind carrying a jammed or upper-bound gate holds its baseline
// pending; anything else has no jam baseline for the successor to retake.
func TestScheduleRestoreLeavesUnjammedBaselineSettled(t *testing.T) {
	cases := []struct {
		name  string
		fresh func() Schedule
		hk    HandoffKind
	}{
		{
			name:  "work kind with a non-jammed gate",
			fresh: func() Schedule { return probedSchedule([]Kind{schedWork}, 0) },
			hk:    HandoffKind{Kind: schedWork, Until: schedT0.Add(time.Minute), Jammed: false},
		},
		{
			name:  "upper-bound kind with no gate",
			fresh: upperBoundSchedule,
			hk:    HandoffKind{Kind: schedRecover, LimitedUntil: schedT0.Add(time.Minute)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			succ := c.fresh().restore([]HandoffKind{c.hk})
			if v := succ.View(c.hk.Kind, schedT0); v.JamBaselinePending {
				t.Fatalf("restored view = %+v, want the baseline settled", v)
			}
		})
	}
}
