package daemon

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/report"
)

func butlerOnlyConfig() Config {
	cfg := testConfig(1)
	cfg.Kinds = []Kind{KindOf(dispatchkind.Butler)}
	return cfg
}

// notDueRun drives a butler-only Loop whose first child reports one not_due
// record carrying nextDue and exits 2, and whose second child start stops the
// run. It returns the clock and runner state seen at that second start, which
// is where the park between the two children ended.
type notDueRun struct {
	clk          *testClock
	secondStart  time.Time // clock reading when the second child started
	secondResolv int       // ResolveTip calls made by then
	calls        int
}

func runNotDue(t *testing.T, base time.Time, nextDue report.NextDue, moved []bool) *notDueRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{}
	clk.setNow(base)
	run := &notDueRun{clk: clk}
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		moved:     moved,
		results:   []ChildResult{{Exit: 2}, {Exit: 2}},
	}
	r.onStart = func(_ context.Context, req ChildRequest) error {
		run.calls++
		if run.calls == 1 {
			req.OnRecord(Record{Event: report.EventNotDue, Key: dispatchkey.Chore("bugs"), NextDue: nextDue})
			return nil
		}
		run.secondStart, run.secondResolv = clk.Now(), r.resolveCount()
		cancel()
		return nil
	}
	var buf bytes.Buffer
	reason := Loop(ctx, butlerOnlyConfig(), r, newTestEmitter(&buf), clk).String()
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want context-cancelled (the test's own stop at the second start)", reason)
	}
	if run.calls != 2 {
		t.Fatalf("butler children started = %d, want exactly 2", run.calls)
	}
	return run
}

// TestLoopButlerParksUntilReportedNextDue pins the issue's headline: a butler
// child that reports its next Chore's due instant and exits 2 parks the
// daemon until exactly that instant, in one sleep, instead of the idle
// backoff's few-millisecond respawn loop.
func TestLoopButlerParksUntilReportedNextDue(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	due := base.Add(2 * time.Hour)
	run := runNotDue(t, base, report.NextDue{At: due}, nil)

	if !run.secondStart.Equal(due) {
		t.Fatalf("second butler start at %v, want the reported next_due %v", run.secondStart, due)
	}
	if got := run.clk.waits(); len(got) != 1 || got[0] != 2*time.Hour {
		t.Fatalf("waits = %v, want one 2h sleep to the reported instant", got)
	}
}

// TestLoopButlerOnTipMoveParksUntilTipMoves: an on_tip_move report keeps the
// butler idle across tip resolutions that report Moved == false and lifts it
// on the first resolution that reports a move.
func TestLoopButlerOnTipMoveParksUntilTipMoves(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	// Call 1 is the first child's own start; calls 2-4 are the park's tip
	// polls and see nothing; call 5 sees the move.
	run := runNotDue(t, base, report.NextDue{OnTipMove: true}, []bool{false, false, false, false, true})

	if run.secondResolv != 5 {
		t.Fatalf("second butler start after %d tip resolutions, want 5 (the first one reporting Moved)", run.secondResolv)
	}
	if n := run.clk.waitCount(); n < 3 {
		t.Fatalf("waits = %v, want the park to sleep between each unmoved poll", run.clk.waits())
	}
	if elapsed := run.secondStart.Sub(base); elapsed >= time.Hour {
		t.Fatalf("second start %v after the report, want it lifted by the moved tip, not the idle cap", elapsed)
	}
}

// TestLoopButlerLiveClaimParksUntilClaimGoesStale feeds the instant
// chore.NextDue computes for a live claim into the report: the butler's next
// start is the first instant the claim is stale, not a nanosecond earlier.
func TestLoopButlerLiveClaimParksUntilClaimGoesStale(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	const timeout = time.Hour
	tip := ledger.Tip{Commit: "c1", State: ledger.State{
		Phase:     ledger.Claimed,
		ClaimedBy: &ledger.ClaimedBy{Host: "h", Start: base.Add(-10 * time.Minute)},
	}}
	cfg := chore.DueConfig{ClaimTimeout: timeout}
	at, onTipMove := chore.NextDue(tip, nil, "head", base, chore.Room{}, cfg)
	if onTipMove || at.IsZero() {
		t.Fatalf("chore.NextDue = (%v, %v), want a timed instant for a live claim", at, onTipMove)
	}
	if tip.State.StaleClaim(at.Add(-1), timeout) || !tip.State.StaleClaim(at, timeout) {
		t.Fatalf("claim staleness does not flip at %v", at)
	}

	run := runNotDue(t, base, report.NextDue{At: at}, nil)

	if !run.secondStart.Equal(at) {
		t.Fatalf("second butler start at %v, want the claim-staleness instant %v", run.secondStart, at)
	}
	if got := run.clk.waits(); len(got) != 1 || got[0] != at.Sub(base) {
		t.Fatalf("waits = %v, want one sleep of %v", got, at.Sub(base))
	}
}

// TestLoopButlerExitTwoWithoutReportStillBacksOff: an older child that
// reports nothing keeps the exit-driven idle backoff.
func TestLoopButlerExitTwoWithoutReportStillBacksOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{}
	calls := 0
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}, {Exit: 2}}}
	r.onStart = func(context.Context, ChildRequest) error {
		if calls++; calls == 2 {
			cancel()
		}
		return nil
	}
	var buf bytes.Buffer
	Loop(ctx, butlerOnlyConfig(), r, newTestEmitter(&buf), clk)

	if got := clk.waits(); len(got) != 1 || got[0] != testIdleFloor {
		t.Fatalf("waits = %v, want one idle-floor backoff %v", got, testIdleFloor)
	}
}

// TestPoolNoteNotDueMergesWithoutClaiming: not_due records fold into the
// slot's flight (earliest instant, tip-move OR'd) but are not a claim — the
// key stays unset — and one arriving after the slot cleared is dropped.
func TestPoolNoteNotDueMergesWithoutClaiming(t *testing.T) {
	cfg := butlerOnlyConfig()
	var buf bytes.Buffer
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), &testClock{})
	defer p.cancel()

	late := time.Date(2026, 1, 2, 5, 0, 0, 0, time.UTC)
	early := late.Add(-time.Hour)
	if _, _, ok := p.startChild(0, KindOf(dispatchkind.Butler), "rev1"); !ok {
		t.Fatal("startChild did not start the butler")
	}
	p.noteNotDue(0, Record{Event: report.EventNotDue, Key: dispatchkey.Chore("a"), NextDue: report.NextDue{At: late}})
	p.noteNotDue(0, Record{Event: report.EventNotDue, Key: dispatchkey.Chore("b"), NextDue: report.NextDue{At: early}})
	p.noteNotDue(0, Record{Event: report.EventNotDue, Key: dispatchkey.Chore("c"), NextDue: report.NextDue{OnTipMove: true}})

	gotDue := p.flightReport(0).nextDue
	if got, want := gotDue, (NextDue{At: early, OnTipMove: true}); !got.At.Equal(want.At) || got.OnTipMove != want.OnTipMove {
		t.Fatalf("flightReport nextDue = %+v, want %+v", got, want)
	}
	if key := p.flightClaim(0); key != (dispatchkey.Key{}) {
		t.Fatalf("flightClaim = %v, want zero: a not_due record is not a claim", key)
	}

	p.finishChild(0)
	p.noteNotDue(0, Record{Event: report.EventNotDue, Key: dispatchkey.Chore("a"), NextDue: report.NextDue{At: early}})
	if got := p.flightReport(0).nextDue; !got.IsZero() {
		t.Fatalf("flightReport nextDue after finish = %+v, want zero: a late record is dropped", got)
	}
}

// TestLoopButlerOnlyPoolStartsEveryChildWithoutTheBaton: a Chore-keyed kind
// skips the discovery baton (issue #4583) — the Ledger's compare-and-swap
// settles races between butler children — so every slot of a butler-only
// Daemon starts its own child at once, none parking on baton_hold.
func TestLoopButlerOnlyPoolStartsEveryChildWithoutTheBaton(t *testing.T) {
	cfg := butlerOnlyConfig()
	cfg.Slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(cfg.Slots)
	clk := &testClock{}
	nw := newNotifyWriter()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Halt, 1)
	go func() { done <- Loop(ctx, cfg, r, newTestEmitter(nw), clk) }()

	seen := map[int]bool{}
	for range cfg.Slots {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != cfg.Slots || r.kindCount(KindOf(dispatchkind.Butler)) != cfg.Slots {
		t.Fatalf("started slots = %v with %d butler children, want one butler child per slot", seen, r.kindCount(KindOf(dispatchkind.Butler)))
	}
	if strings.Contains(nw.String(), "\"event\":\"baton_hold\"") {
		t.Fatal("a butler slot emitted baton_hold, want butler starts to skip the baton")
	}

	cancel()
	for slot := 0; slot < cfg.Slots; slot++ {
		r.releaseSlot(t, slot, ChildResult{Exit: 2})
	}
	<-done
}

// TestLoopButlerTipMoveDuringChildIsNotParkedOn pins the stale-report race
// on a tip move: a work slot's merge moves the tip while the butler child is
// still running, so noteTipMoved finds nothing parked to lift and the pool's
// baseline advances anyway. The child, run at the old revision, then reports
// on_tip_move; parking on it would wait for a move already spent.
func TestLoopButlerTipMoveDuringChildIsNotParkedOn(t *testing.T) {
	work, butler := KindOf(dispatchkind.Work), KindOf(dispatchkind.Butler)
	cfg := testConfig(2)
	cfg.Kinds = []Kind{work, butler}
	// Resolves 1-2 are the slots' first, 3 is slot 1 after its empty work
	// child, 4 is slot 0 after its merge and sees the move; later calls
	// see nothing further.
	r := &scriptedRunner{
		revisions: []string{"rev1", "rev1", "rev1", "rev2"},
		moved:     []bool{false, false, false, true, false},
	}
	r.holdSlots(cfg.Slots)
	nw := newNotifyWriter()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Halt, 1)
	go func() { done <- Loop(ctx, cfg, r, newTestEmitter(nw), &testClock{}) }()

	// Slot 0's work child claims and keeps running; slot 1 then finds the
	// work queue empty and falls through to a butler child.
	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("first child on slot %d, want 0", got)
	}
	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	r.fireOnIssue(t, 0, "42")
	if got := r.awaitStart(t); got != 1 {
		t.Fatalf("second child on slot %d, want 1", got)
	}
	r.releaseSlot(t, 1, ChildResult{Exit: 2})
	if got := r.awaitStart(t); got != 1 || r.kindCount(butler) != 1 {
		t.Fatalf("third child on slot %d with %d butler children, want the butler on slot 1", got, r.kindCount(butler))
	}
	r.fireOnRecord(t, 1, Record{Event: report.EventNotDue, Key: dispatchkey.Chore("bugs"), NextDue: report.NextDue{OnTipMove: true}})

	// The merge lands: slot 0 resolves the moved tip and starts its next
	// child, held from here on (its Box announced, so the baton passes) so
	// only slot 1 cycles below. Slot 1's butler child then exits with its
	// on_tip_move report, run at the old revision.
	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("child after the merge on slot %d, want 0", got)
	}
	r.fireOnIssue(t, 0, "43")
	r.releaseSlot(t, 1, ChildResult{Exit: 2})

	butlerOnSlot1 := func() (n int) {
		for _, c := range r.calls() {
			if c.Kind == butler && c.Slot == 1 {
				n++
			}
		}
		return n
	}
	for i := 0; butlerOnSlot1() < 2; i++ {
		if i == 20 {
			t.Fatal("butler did not start again after its on_tip_move report went stale; it parked on a spent tip move")
		}
		slot := r.awaitStart(t)
		if butlerOnSlot1() < 2 || slot != 1 {
			r.releaseSlot(t, slot, ChildResult{Exit: 2})
		}
	}
	cancel()
	for slot := 0; slot < cfg.Slots; slot++ {
		select {
		case r.release[slot] <- ChildResult{Exit: 2}:
		default:
		}
	}
	<-done
}

// TestPoolNotDueFromChildStaleAfterSiblingContinue: a butler child whose
// sibling finished with Continue while it ran reports against state that
// sibling already cleared (it may have released the claim the report names),
// so the report is stale and the kind starts again at once.
func TestPoolNotDueFromChildStaleAfterSiblingContinue(t *testing.T) {
	butler := KindOf(dispatchkind.Butler)
	for _, tt := range []struct {
		name         string
		siblingEnds  bool
		wantStartNow bool
	}{
		{name: "sibling continued while the child ran", siblingEnds: true, wantStartNow: true},
		{name: "no sibling continue", siblingEnds: false, wantStartNow: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := butlerOnlyConfig()
			cfg.Slots = 2
			clk := &testClock{}
			clk.setNow(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
			var buf bytes.Buffer
			p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), clk)
			defer p.cancel()

			p.startChild(0, butler, "rev1")
			p.startChild(1, butler, "rev1")
			due := clk.Now().Add(time.Hour)
			p.noteNotDue(1, Record{Event: report.EventNotDue, Key: dispatchkey.Chore("a"), NextDue: report.NextDue{At: due}})
			if tt.siblingEnds {
				p.finishChild(0)
				p.resetKind(butler)
			}
			flight := p.flightReport(1)
			p.finishChild(1)
			p.noteWaitResult(1, butler, "rev1", false, flight)

			p.mu.Lock()
			v := p.st.sched.View(butler, clk.Now())
			p.mu.Unlock()
			if v.Gated == tt.wantStartNow {
				t.Fatalf("butler Gated = %v, want %v", v.Gated, !tt.wantStartNow)
			}
		})
	}
}
