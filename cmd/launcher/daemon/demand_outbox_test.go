package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/recoverrecord"
	"spindrift.dev/launcher/internal/seambundle"
)

var recoverKind = daemon.KindOf(dispatchkind.Recover)
var workKind = daemon.KindOf(dispatchkind.Work)

// seedOutboxBundleT drops a bundle for issue num under the cwd's outbox, as a
// finished read-only Box leaves it.
func seedOutboxBundleT(t *testing.T, num string) {
	t.Helper()
	dir := hostpaths.OutboxDir("", num)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(dir, seambundle.FileName), "bundle-"+num)
}

// seedAttemptT records count failed recover attempts for num, the last at last.
func seedAttemptT(t *testing.T, num string, count int, last time.Time) {
	t.Helper()
	id, err := recoverrecord.BundleID("", num)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hostpaths.LogDir(""), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := recoverrecord.Load("", num, id)
	rec.Count, rec.Last = count, last
	if err := rec.Save(); err != nil {
		t.Fatal(err)
	}
}

// seedGaveUpT is seedAttemptT for a bundle whose give-up comment posted.
func seedGaveUpT(t *testing.T, num string, count int, last time.Time) {
	t.Helper()
	seedAttemptT(t, num, count, last)
	id, err := recoverrecord.BundleID("", num)
	if err != nil {
		t.Fatal(err)
	}
	rec := recoverrecord.Load("", num, id)
	rec.GaveUp = true
	if err := rec.Save(); err != nil {
		t.Fatal(err)
	}
}

func outboxCounterT(t *testing.T, settings map[string]string, now time.Time) forge.DemandCounter {
	t.Helper()
	clearKnobEnvT(t)
	t.Chdir(t.TempDir())
	src, _ := buildDemandSources(demandDocT(settings), []daemon.Kind{recoverKind})
	c, ok := src[recoverKind].(*outboxDemand)
	if !ok {
		t.Fatalf("recover source = %T, want *outboxDemand", src[recoverKind])
	}
	c.now = func() time.Time { return now }
	return c
}

func TestOutboxDemand_CountsOnlyEligibleBundles(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := outboxCounterT(t, nil, now)
	count := func() int {
		t.Helper()
		n, err := c.CountReady(false)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	if got := count(); got != 0 {
		t.Fatalf("empty outbox: Demand = %d, want 0", got)
	}
	seedOutboxBundleT(t, "1")
	if got := count(); got != 1 {
		t.Fatalf("fresh bundle: Demand = %d, want 1", got)
	}
	seedOutboxBundleT(t, "2")
	seedAttemptT(t, "2", 1, now.Add(-time.Second))
	if got := count(); got != 1 {
		t.Fatalf("bundle in backoff: Demand = %d, want it not counted", got)
	}
	seedOutboxBundleT(t, "3")
	seedGaveUpT(t, "3", 3, now.Add(-24*time.Hour))
	if got := count(); got != 1 {
		t.Fatalf("gave-up bundle: Demand = %d, want it not counted", got)
	}
	// At the bound with no give-up comment posted: the child must run to retry it.
	seedOutboxBundleT(t, "4")
	seedAttemptT(t, "4", 3, now.Add(-24*time.Hour))
	if got := count(); got != 2 {
		t.Fatalf("bundle at the bound, give-up not posted: Demand = %d, want it counted", got)
	}
	if got := c.ProbeInterval(); got != 20*time.Second {
		t.Errorf("ProbeInterval() = %v, want 20s", got)
	}
}

func TestOutboxDemand_HonoursTheChildKnobs(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := outboxCounterT(t, map[string]string{"MAX_RECOVER_ATTEMPTS": "5", "TRANSIENT_BACKOFF_SECS": "1"}, now)
	seedOutboxBundleT(t, "1")
	// A 10s-old failure at Count 2 is inside the default backoff (30s times the
	// count, 60s); the document's 1s unit lets it out.
	seedAttemptT(t, "1", 2, now.Add(-10*time.Second))
	seedOutboxBundleT(t, "2")
	seedAttemptT(t, "2", 3, now.Add(-time.Second/2))
	if got, err := c.CountReady(false); err != nil || got != 1 {
		t.Fatalf("Demand = %d, %v, want 1: the 1s backoff releases bundle 1, and bundle 2 is still backing off below MAX_RECOVER_ATTEMPTS=5", got, err)
	}
}

func TestHostRunnerDemand_OutboxSkipsKeysThePoolHolds(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := outboxCounterT(t, nil, now)
	seedOutboxBundleT(t, "7")
	r := &hostRunner{demand: demandSources{recoverKind: c, workKind: fixedReady(4)}}
	ready := func(kind daemon.Kind, inFlight map[dispatchkey.Key]bool) int {
		t.Helper()
		d, err := r.Demand(context.Background(), kind, false, inFlight)
		if err != nil {
			t.Fatal(err)
		}
		return d.Ready
	}

	if got := ready(recoverKind, nil); got != 1 {
		t.Errorf("no children: Ready = %d, want 1", got)
	}
	if got := ready(recoverKind, map[dispatchkey.Key]bool{dispatchkey.Issue("7"): true}); got != 0 {
		t.Errorf("a child holds issue 7: Ready = %d, want 0", got)
	}
	if d, err := r.Demand(context.Background(), recoverKind, false, map[dispatchkey.Key]bool{dispatchkey.Issue("7"): true}); err != nil || len(d.IDs) != 0 {
		t.Errorf("a child holds issue 7: IDs = %v, %v, want none", d.IDs, err)
	}
	if got := ready(recoverKind, map[dispatchkey.Key]bool{dispatchkey.Issue("8"): true}); got != 1 {
		t.Errorf("a child holds another issue: Ready = %d, want 1", got)
	}
	if got := ready(workKind, map[dispatchkey.Key]bool{dispatchkey.Issue("7"): true}); got != 4 {
		t.Errorf("tracker counter: Ready = %d, want its plain CountReady 4", got)
	}
}

type fixedReady int

func (f fixedReady) CountReady(bool) (int, error) { return int(f), nil }
func (fixedReady) ProbeInterval() time.Duration   { return time.Second }

func TestLauncherInt_MatchesTheLaunchersParse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		doc     map[string]string
		ambient string
		want    int
	}{
		{"unset is the schema default", nil, "", 3},
		{"ambient positive", nil, "7", 7},
		{"ambient zero falls back", nil, "0", 3},
		{"ambient negative falls back", nil, "-2", 3},
		{"ambient garbage falls back", nil, "soon", 3},
		{"document positive", map[string]string{"MAX_RECOVER_ATTEMPTS": "5"}, "", 5},
		{"document wins over ambient", map[string]string{"MAX_RECOVER_ATTEMPTS": "5"}, "9", 5},
		// The launcher's schemaDefault is the document's value, parsed without
		// the positive check, so a document "0" is 0, not the schema's 3.
		{"document zero stays zero", map[string]string{"MAX_RECOVER_ATTEMPTS": "0"}, "", 0},
		{"document garbage is zero", map[string]string{"MAX_RECOVER_ATTEMPTS": "soon"}, "", 0},
		{"document zero ignores ambient", map[string]string{"MAX_RECOVER_ATTEMPTS": "0"}, "9", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := launcherInt(demandDocT(tc.doc), "MAX_RECOVER_ATTEMPTS", tc.ambient); got != tc.want {
				t.Errorf("launcherInt = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBuildDemandSources_RecoverGetsOutboxSourceWhateverTheTracker(t *testing.T) {
	for _, tracker := range []string{"bogus", "forgejo", "local", ""} {
		t.Run(tracker, func(t *testing.T) {
			clearKnobEnvT(t)
			src, missing := buildDemandSources(demandDocT(map[string]string{"ISSUE_TRACKER": tracker}), []daemon.Kind{recoverKind})
			if _, ok := src[recoverKind].(*outboxDemand); !ok {
				t.Errorf("recover source = %T, want *outboxDemand", src[recoverKind])
			}
			if len(missing) != 0 {
				t.Errorf("missing = %v, want none: recover needs no tracker", missing)
			}
		})
	}
}

func TestBuildDemandSources_RecoverAlongsideBrokenTracker(t *testing.T) {
	clearKnobEnvT(t)
	src, missing := buildDemandSources(demandDocT(map[string]string{"ISSUE_TRACKER": "bogus"}), []daemon.Kind{workKind, recoverKind})
	if _, ok := src[recoverKind]; !ok {
		t.Error("recover lost its outbox source to the unknown tracker")
	}
	if _, ok := src[workKind]; ok {
		t.Error("work got a source from an unknown tracker")
	}
	if _, ok := missing[workKind]; !ok || len(missing) != 1 {
		t.Errorf("missing = %v, want only work", missing)
	}
}

func TestTrackers_OutboxKindSharesTheTrackerPause(t *testing.T) {
	clearKnobEnvT(t)
	src := demandSources{workKind: fixedInterval(time.Second), recoverKind: &outboxDemand{}}
	got := trackers(src, demandDocT(map[string]string{"ISSUE_TRACKER": "forgejo"}))
	if len(got) != 2 || got[workKind] != "forgejo" || got[recoverKind] != "forgejo" {
		t.Errorf("trackers = %v, want both kinds on forgejo: recover's children call the tracker", got)
	}
}

// wiringClock is a fake clock whose Sleep advances time and runs hook, so a
// test seeds the outbox at a chosen instant.
type wiringClock struct {
	mu    sync.Mutex
	now   time.Time
	hook  func(sleeps int)
	count int
}

func (c *wiringClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *wiringClock) Sleep(ctx context.Context, d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.count++
	n := c.count
	c.mu.Unlock()
	c.hook(n)
}

// wiringRunner serves Demand from the same sources the production runner does.
type wiringRunner struct {
	demand  demandSources
	onStart func(daemon.Kind)
}

func (r *wiringRunner) ResolveTip(context.Context) (daemon.Tip, error) {
	return daemon.Tip{Revision: "rev1"}, nil
}

func (r *wiringRunner) Demand(ctx context.Context, k daemon.Kind, fresh bool, inFlight map[dispatchkey.Key]bool) (daemon.Demand, error) {
	return (&hostRunner{demand: r.demand}).Demand(ctx, k, fresh, inFlight)
}

func (r *wiringRunner) RunChild(_ context.Context, req daemon.ChildRequest) (daemon.ChildResult, error) {
	r.onStart(req.Kind)
	return daemon.ChildResult{}, nil
}

// TestRecoverOutboxDemand_DrivesTheLoopThroughTheDaemonWiring assembles the
// pool Config from the same buildDemandSources, probeIntervals and trackers
// calls mainRun makes, over a real temp outbox, and checks the schedule: no
// recover child while the outbox is empty or holds only a bundle in backoff
// or one whose give-up posted, and one within a probe interval of an eligible
// bundle appearing.
func TestRecoverOutboxDemand_DrivesTheLoopThroughTheDaemonWiring(t *testing.T) {
	clearKnobEnvT(t)
	t.Chdir(t.TempDir())
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &wiringClock{now: t0}

	doc := demandDocT(map[string]string{"TRANSIENT_BACKOFF_SECS": "3600"})
	src, _ := buildDemandSources(doc, []daemon.Kind{recoverKind})
	src[recoverKind].(*outboxDemand).now = clk.Now
	cfg := daemon.Config{
		Kinds:            []daemon.Kind{recoverKind},
		ProbeIntervals:   probeIntervals(src, 0),
		Trackers:         trackers(src, doc),
		Slots:            1,
		IdleFloor:        time.Minute,
		IdleCap:          time.Hour,
		FailureBackoff:   time.Minute,
		BreakerThreshold: 3,
		BreakerWindow:    time.Hour,
	}
	interval := cfg.ProbeIntervals[recoverKind]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var starts []time.Time
	runner := &wiringRunner{demand: src, onStart: func(k daemon.Kind) {
		if k == recoverKind {
			starts = append(starts, clk.Now())
		}
		cancel()
	}}
	var appeared time.Time
	startsBefore := -1
	clk.hook = func(n int) {
		switch n {
		case 1:
			seedOutboxBundleT(t, "2")
			seedAttemptT(t, "2", 1, t0)
			seedOutboxBundleT(t, "3")
			seedGaveUpT(t, "3", 3, t0.Add(-24*time.Hour))
		case 6:
			startsBefore = len(starts)
			appeared = clk.Now()
			seedOutboxBundleT(t, "4")
		case 40:
			cancel()
		}
	}

	var out bytes.Buffer
	daemon.Loop(ctx, cfg, runner, daemon.NewEmitter(&out, &out, clk.Now), clk)

	if startsBefore != 0 {
		t.Fatalf("recover children started before an eligible bundle appeared = %d, want 0", startsBefore)
	}
	if len(starts) != 1 {
		t.Fatalf("recover children started = %d, want exactly 1 (log: %s)", len(starts), out.String())
	}
	if got := starts[0].Sub(appeared); got > interval {
		t.Errorf("recover started %s after the bundle appeared, want within one probe interval %s", got, interval)
	}
}

func TestHostRunnerDemand_OutboxNamesItsBundles(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := outboxCounterT(t, nil, now)
	r := mustHostRunner(t, hostRunnerConfig{env: []string{}, demand: map[daemon.Kind]forge.DemandCounter{recoverKind: c}})
	demand := func() daemon.Demand {
		t.Helper()
		d, err := r.Demand(context.Background(), recoverKind, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	seedOutboxBundleT(t, "1")
	seedOutboxBundleT(t, "2")
	before := demand()
	if before.Ready != 2 || len(before.IDs) != 2 {
		t.Fatalf("Demand = %+v, want Ready 2 with two IDs", before)
	}

	writeFileT(t, filepath.Join(hostpaths.OutboxDir("", "1"), seambundle.FileName), "a replacement bundle")
	after := demand()
	if len(after.IDs) != 2 {
		t.Fatalf("Demand = %+v, want two IDs", after)
	}
	if after.Ready != before.Ready || after.IDs[0] == before.IDs[0] || after.IDs[1] != before.IDs[1] {
		t.Errorf("replaced bundle 1: %+v -> %+v, want same Ready and only key 1's ID changed", before, after)
	}
}
