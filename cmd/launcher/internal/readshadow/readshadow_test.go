package readshadow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
)

// opLog collects the ops a Shadow emits.
type opLog struct {
	mu  sync.Mutex
	ops []hostop.SpindriftOp
}

func (l *opLog) emit(op hostop.SpindriftOp) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops = append(l.ops, op)
}

func (l *opLog) summaries() []hostop.ShadowSummary {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []hostop.ShadowSummary
	for _, op := range l.ops {
		if op.ShadowSummary != nil {
			out = append(out, *op.ShadowSummary)
		}
	}
	return out
}

func (l *opLog) divergences() []hostop.ShadowDivergence {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []hostop.ShadowDivergence
	for _, op := range l.ops {
		if op.Divergence != nil {
			out = append(out, *op.Divergence)
		}
	}
	return out
}

// stubTracker is a Connector-side tracker whose reads count and can be scripted.
type stubTracker struct {
	forge.IssueTracker
	calls    atomic.Int64
	byOp     sync.Map // op -> *atomic.Int64
	listFn   func() ([]forge.Issue, error)
	issueFn  func(string) (forge.Issue, error)
	onCall   func()
	callWait time.Duration
}

func (s *stubTracker) count(op string) {
	s.calls.Add(1)
	c, _ := s.byOp.LoadOrStore(op, new(atomic.Int64))
	c.(*atomic.Int64).Add(1)
	if s.onCall != nil {
		s.onCall()
	}
	time.Sleep(s.callWait)
}

func (s *stubTracker) perOp(op string) int64 {
	c, ok := s.byOp.Load(op)
	if !ok {
		return 0
	}
	return c.(*atomic.Int64).Load()
}

func (s *stubTracker) ListIssues(forge.DispatchState) ([]forge.Issue, error) {
	s.count("ListIssues")
	if s.listFn != nil {
		return s.listFn()
	}
	return nil, nil
}

func (s *stubTracker) ListOpenIssues() ([]forge.Issue, error) {
	s.count("ListOpenIssues")
	return nil, nil
}

func (s *stubTracker) Issue(num string) (forge.Issue, error) {
	s.count("Issue")
	if s.issueFn != nil {
		return s.issueFn(num)
	}
	return forge.Issue{Number: num}, nil
}

// stubPR is a Connector-side PRForge answering the five shadowed reads.
type stubPR struct {
	forge.PRForge
	pr    forge.PR
	state forge.PRState
}

func (s *stubPR) OpenPRForBranch(string) (forge.PR, bool, error) { return s.pr, true, nil }
func (s *stubPR) PRForBranch(string) (string, bool, error)       { return s.pr.URL, true, nil }
func (s *stubPR) PRState(string) (forge.PRState, error)          { return s.state, nil }
func (s *stubPR) CheckState(string) (forge.RollupState, error)   { return "SUCCESS", nil }
func (s *stubPR) FailureDetail(string) (string, error)           { return "", nil }

type rig struct {
	*Shadow
	log   *opLog
	warn  *strings.Builder
	opens atomic.Int64
}

func newRig(t *testing.T, every int, open func(ctx context.Context, observe func(connectorhost.AuditEntry)) (Reads, error)) *rig {
	t.Helper()
	r := &rig{log: &opLog{}, warn: &strings.Builder{}}
	r.Shadow = New(Config{
		Backend: "github", Every: every, Emit: r.log.emit, Warn: syncWriter{r.warn},
		Open: func(ctx context.Context, observe func(connectorhost.AuditEntry)) (Reads, error) {
			r.opens.Add(1)
			return open(ctx, observe)
		},
	})
	return r
}

type syncWriter struct{ b *strings.Builder }

var warnMu sync.Mutex

func (w syncWriter) Write(p []byte) (int, error) {
	warnMu.Lock()
	defer warnMu.Unlock()
	return w.b.Write(p)
}

func opensWith(reads Reads) func(context.Context, func(connectorhost.AuditEntry)) (Reads, error) {
	return func(context.Context, func(connectorhost.AuditEntry)) (Reads, error) { return reads, nil }
}

func (r *rig) closeAndSummary(t *testing.T) hostop.ShadowSummary {
	t.Helper()
	r.Close(context.Background())
	sums := r.log.summaries()
	if len(sums) != 1 {
		t.Fatalf("summaries = %d, want exactly 1", len(sums))
	}
	return sums[0]
}

func seededFake() *forge.Fake {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "7", Title: "native title"})
	return f
}

func TestDivergentConnectorRecordsDivergenceAndCallerGetsNative(t *testing.T) {
	st := &stubTracker{issueFn: func(num string) (forge.Issue, error) {
		return forge.Issue{Number: num, Title: "connector title"}, nil
	}}
	r := newRig(t, 1, opensWith(Reads{Tracker: st}))
	tr := r.Tracker(seededFake())

	got, err := tr.Issue("7")
	if err != nil || got.Title != "native title" {
		t.Fatalf("Issue = %+v, %v; want the native result", got, err)
	}
	sum := r.closeAndSummary(t)

	divs := r.log.divergences()
	if len(divs) != 1 {
		t.Fatalf("divergences = %d, want 1", len(divs))
	}
	d := divs[0]
	if d.Check != "Issue" || d.Driver != "github" || !strings.Contains(d.Line, `"7"`) ||
		!strings.Contains(string(d.Native), "native title") || !strings.Contains(string(d.Shadow), "connector title") {
		t.Fatalf("divergence = %+v", d)
	}
	if !sum.Loaded || sum.Compared != 1 || sum.Diverged != 1 || sum.Error != "" {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestAgreeingConnectorRecordsNothing(t *testing.T) {
	st := &stubTracker{issueFn: func(num string) (forge.Issue, error) {
		return forge.Issue{Number: num, Title: "native title"}, nil
	}}
	r := newRig(t, 1, opensWith(Reads{Tracker: st}))
	_, _ = r.Tracker(seededFake()).Issue("7")
	sum := r.closeAndSummary(t)
	if sum.Compared != 1 || sum.Diverged != 0 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestListOrderIsNotADivergence(t *testing.T) {
	st := &stubTracker{listFn: func() ([]forge.Issue, error) {
		return []forge.Issue{{Number: "10"}, {Number: "2"}}, nil
	}}
	r := newRig(t, 1, opensWith(Reads{Tracker: st}))
	native := &fixedList{IssueTracker: forge.NewFake(), list: []forge.Issue{{Number: "2"}, {Number: "10"}}}
	_, _ = r.Tracker(native).ListIssues(forge.Dispatchable)
	if sum := r.closeAndSummary(t); sum.Compared != 1 || sum.Diverged != 0 {
		t.Fatalf("summary = %+v, want a reordered list to agree", sum)
	}
}

type fixedList struct {
	forge.IssueTracker
	list []forge.Issue
}

func (f *fixedList) ListIssues(forge.DispatchState) ([]forge.Issue, error) { return f.list, nil }

func TestEmptyListNilVersusEmptyAgrees(t *testing.T) {
	st := &stubTracker{listFn: func() ([]forge.Issue, error) { return []forge.Issue{}, nil }}
	r := newRig(t, 1, opensWith(Reads{Tracker: st}))
	_, _ = r.Tracker(&fixedList{IssueTracker: forge.NewFake()}).ListIssues(forge.Dispatchable)
	if sum := r.closeAndSummary(t); sum.Compared != 1 || sum.Diverged != 0 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestBothFailingAgreesOneFailingDiverges(t *testing.T) {
	f := forge.NewFake()
	f.IssueErrByNum = map[string]error{"1": errors.New("native boom"), "2": errors.New("native boom")}
	st := &stubTracker{issueFn: func(num string) (forge.Issue, error) {
		if num == "1" {
			return forge.Issue{}, errors.New("different words")
		}
		return forge.Issue{Number: num}, nil
	}}
	r := newRig(t, 1, opensWith(Reads{Tracker: st}))
	tr := r.Tracker(f)
	if _, err := tr.Issue("1"); err == nil || err.Error() != "native boom" {
		t.Fatalf("native error not returned untouched: %v", err)
	}
	_, _ = tr.Issue("2")
	sum := r.closeAndSummary(t)
	divs := r.log.divergences()
	if sum.Compared != 2 || len(divs) != 1 || !strings.Contains(divs[0].Line, `"2"`) {
		t.Fatalf("summary = %+v, divergences = %+v; want only issue 2 to diverge", sum, divs)
	}
}

func TestSamplingEveryNthPerOperation(t *testing.T) {
	st := &stubTracker{}
	r := newRig(t, 3, opensWith(Reads{Tracker: st}))
	tr := r.Tracker(seededFake())
	for range 10 {
		_, _ = tr.ListIssues(forge.Dispatchable)
	}
	for range 4 {
		_, _ = tr.Issue("7")
	}
	r.closeAndSummary(t)
	if got := st.perOp("ListIssues"); got != 4 {
		t.Fatalf("ListIssues shadow calls = %d, want 4 (calls 0,3,6,9)", got)
	}
	if got := st.perOp("Issue"); got != 2 {
		t.Fatalf("Issue shadow calls = %d, want 2 (counted separately; calls 0,3)", got)
	}
}

func TestOpenFailureIsLoggedAndNativeUnaffected(t *testing.T) {
	r := newRig(t, 1, func(context.Context, func(connectorhost.AuditEntry)) (Reads, error) {
		return Reads{}, errors.New("route refused")
	})
	tr := r.Tracker(seededFake())
	for range 3 {
		if got, err := tr.Issue("7"); err != nil || got.Title != "native title" {
			t.Fatalf("Issue = %+v, %v", got, err)
		}
	}
	sum := r.closeAndSummary(t)
	if sum.Loaded || !strings.Contains(sum.Error, "route refused") || sum.Compared != 0 || sum.Skipped != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if r.opens.Load() != 1 {
		t.Fatalf("Open called %d times, want once", r.opens.Load())
	}
}

func TestOpenPanicIsContained(t *testing.T) {
	r := newRig(t, 1, func(context.Context, func(connectorhost.AuditEntry)) (Reads, error) { panic("kaboom") })
	_, _ = r.Tracker(seededFake()).Issue("7")
	sum := r.closeAndSummary(t)
	if !strings.HasPrefix(sum.Error, "panic: kaboom") {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestFailClosedConnectorStopsSampling(t *testing.T) {
	st := &stubTracker{}
	var failed atomic.Bool
	st.onCall = func() { failed.Store(true) }
	r := newRig(t, 1, opensWith(Reads{Tracker: st, Failed: func() error {
		if failed.Load() {
			return errors.New("module closed")
		}
		return nil
	}}))
	tr := r.Tracker(seededFake())
	_, _ = tr.Issue("7")
	// Wait until the worker has seen the failure, so later calls are dropped.
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		stopped := r.stopped
		r.mu.Unlock()
		if stopped || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for range 5 {
		_, _ = tr.Issue("7")
	}
	sum := r.closeAndSummary(t)
	if !strings.Contains(sum.Error, "module closed") || st.calls.Load() != 1 {
		t.Fatalf("summary = %+v, connector calls = %d; want one call and a fail-closed error", sum, st.calls.Load())
	}
}

func TestBlockedConnectorDoesNotDelayNativeAndCloseAborts(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	st := &stubTracker{onCall: func() { once.Do(func() { close(entered) }); <-release }}
	var aborted, closed atomic.Bool
	r := newRig(t, 1, opensWith(Reads{
		Tracker: st,
		Abort:   func() { aborted.Store(true); close(release) },
		Close:   func() { closed.Store(true) },
	}))
	tr := r.Tracker(seededFake())

	start := time.Now()
	for range 5 {
		_, _ = tr.Issue("7")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("native calls took %v with a hung shadow", d)
	}
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start = time.Now()
	r.Close(ctx)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Close took %v", d)
	}
	sums := r.log.summaries()
	if len(sums) != 1 || !strings.Contains(sums[0].Error, "shadow deadline exceeded after") {
		t.Fatalf("summaries = %+v", sums)
	}
	if !aborted.Load() || !closed.Load() {
		t.Fatalf("abort = %v, close = %v; want both", aborted.Load(), closed.Load())
	}
}

func TestFullQueueCountsSkipped(t *testing.T) {
	release := make(chan struct{})
	st := &stubTracker{onCall: func() { <-release }}
	r := newRig(t, 1, opensWith(Reads{Tracker: st}))
	tr := r.Tracker(seededFake())
	const total = queueSize + 20
	for range total {
		_, _ = tr.Issue("7")
	}
	close(release)
	sum := r.closeAndSummary(t)
	if sum.Skipped < 1 || sum.Compared+sum.Skipped != total {
		t.Fatalf("summary = %+v, want Skipped > 0 and Compared+Skipped == %d", sum, total)
	}
}

func TestObserveTalliesWarnsOnceAndRateLimitSuspends(t *testing.T) {
	var observe func(connectorhost.AuditEntry)
	st := &stubTracker{}
	st.onCall = func() {
		forbidden := connectorhost.AuditEntry{Method: "GET", Host: "api.github.com", Path: "/repos/o/r/issues/7", Status: 403}
		observe(forbidden)
		observe(forbidden)
		observe(connectorhost.AuditEntry{Method: "GET", Host: "api.github.com", Path: "/p", Status: 403, RateLimit: "primary"})
		sec := connectorhost.AuditEntry{Method: "GET", Host: "api.github.com", Path: "/s", Status: 403, RateLimit: "secondary", RetryAfter: "60"}
		observe(sec)
		observe(sec)
		observe(connectorhost.AuditEntry{Status: 200})
	}
	r := newRig(t, 1, func(_ context.Context, o func(connectorhost.AuditEntry)) (Reads, error) {
		observe = o
		return Reads{Tracker: st}, nil
	})
	tr := r.Tracker(seededFake())
	_, _ = tr.Issue("7")
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		stopped := r.stopped
		r.mu.Unlock()
		if stopped || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for range 5 {
		_, _ = tr.Issue("7")
	}
	sum := r.closeAndSummary(t)

	if sum.Forbidden != 2 || sum.RateLimited != 1 || sum.SecondaryRateLimited != 2 {
		t.Fatalf("summary = %+v", sum)
	}
	if !strings.Contains(sum.Error, "rate limit") || st.calls.Load() != 1 {
		t.Fatalf("summary = %+v, connector calls = %d; want a rate limit to suspend sampling", sum, st.calls.Load())
	}
	warnMu.Lock()
	warn := r.warn.String()
	warnMu.Unlock()
	lines := strings.Split(strings.TrimSpace(warn), "\n")
	if len(lines) != 3 {
		t.Fatalf("warn lines = %q, want one per kind", lines)
	}
	if !strings.Contains(warn, "secondary rate limit on GET api.github.com/s (retry-after 60)") {
		t.Fatalf("warn = %q", warn)
	}
}

type plainCodeForge struct{ forge.CodeForge }

func TestCapabilitiesShowThrough(t *testing.T) {
	r := newRig(t, 1, opensWith(Reads{}))
	native := forge.NewFake()

	tr := r.Tracker(native)
	if _, ok := forge.As[forge.CommentLister](tr); !ok {
		t.Fatal("forge.As does not find the native tracker's CommentLister through the layer")
	}
	if u, ok := tr.(forge.Unwrapper); !ok || u.Unwrap() != forge.IssueTracker(native) {
		t.Fatal("tracker layer does not unwrap to native")
	}

	cf := r.CodeForge(native)
	caps := forge.ResolveCapabilities(cf, tr, backend.Descriptor{}, backend.Descriptor{})
	if _, isNative := caps.PRForge.(*forge.Fake); caps.PRForge == nil || isNative {
		t.Fatalf("caps.PRForge = %T, want the shadow layer", caps.PRForge)
	}
	if caps.BranchProtectionForge == nil {
		t.Fatal("native BranchProtectionForge does not reach ResolveCapabilities")
	}

	bare := plainCodeForge{native.CodeForgeFake}
	if got := r.CodeForge(bare); got != forge.CodeForge(bare) {
		t.Fatalf("CodeForge without PRForge = %T, want it returned unchanged", got)
	}
	r.Close(context.Background())
}

func TestPRReadsAreShadowedAndRestDelegates(t *testing.T) {
	native := forge.NewFake()
	native.SetPR("agent/issue-7", forge.PR{URL: "https://x/pr/1"})
	native.SetPRState("https://x/pr/1", forge.PROpen)
	st := &stubPR{pr: forge.PR{URL: "https://x/pr/2"}, state: forge.PROpen}
	r := newRig(t, 1, opensWith(Reads{PR: st}))
	cf := r.CodeForge(native)
	pf, _ := forge.AsCodeForge[forge.PRForge](cf)

	pr, ok, err := pf.OpenPRForBranch("agent/issue-7")
	if err != nil || !ok || pr.URL != "https://x/pr/1" {
		t.Fatalf("OpenPRForBranch = %+v %v %v; want native", pr, ok, err)
	}
	_, _, _ = pf.PRForBranch("agent/issue-7")
	_, _ = pf.PRState("https://x/pr/1")
	_, _ = pf.CheckState("https://x/pr/1")
	_, _ = pf.FailureDetail("https://x/pr/1")
	if _, err := pf.Mergeable("https://x/pr/1"); err != nil {
		t.Fatalf("Mergeable delegates to native, got %v", err)
	}
	sum := r.closeAndSummary(t)
	if sum.Compared != 5 {
		t.Fatalf("summary = %+v, want the five shadowed reads compared (Mergeable is not)", sum)
	}
	var checks []string
	for _, d := range r.log.divergences() {
		checks = append(checks, d.Check)
	}
	if len(checks) < 2 || checks[0] != "OpenPRForBranch" {
		t.Fatalf("divergence checks = %v, want OpenPRForBranch (and PRForBranch) to diverge", checks)
	}
}

type slowNative struct{ forge.IssueTracker }

func (slowNative) Issue(num string) (forge.Issue, error) {
	time.Sleep(time.Millisecond)
	return forge.Issue{Number: num}, nil
}

func TestCloseEmitsOneSummaryWithMeanTimings(t *testing.T) {
	st := &stubTracker{callWait: 2 * time.Millisecond}
	r := newRig(t, 1, opensWith(Reads{Tracker: st}))
	tr := r.Tracker(slowNative{forge.NewFake()})
	_, _ = tr.Issue("1")
	_, _ = tr.Issue("2")
	r.Close(context.Background())
	r.Close(context.Background())
	sums := r.log.summaries()
	if len(sums) != 1 {
		t.Fatalf("summaries = %d, want 1 after two Close calls", len(sums))
	}
	s := sums[0]
	if s.Compared != 2 || s.NativeNsPerLine < int64(time.Millisecond) || s.ShadowNsPerLine < int64(2*time.Millisecond) {
		t.Fatalf("summary = %+v", s)
	}
}

func TestCloseWithNothingSampled(t *testing.T) {
	r := newRig(t, 5, opensWith(Reads{}))
	sum := r.closeAndSummary(t)
	if sum.Loaded || sum.Compared != 0 || sum.Driver != "github" || r.opens.Load() != 0 {
		t.Fatalf("summary = %+v, opens = %d", sum, r.opens.Load())
	}
}

func TestConnectorOpenedAfterCloseDeadlineIsStillClosed(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan struct{})
	r := newRig(t, 1, func(context.Context, func(connectorhost.AuditEntry)) (Reads, error) {
		close(entered)
		<-release
		return Reads{Tracker: &stubTracker{}, Close: func() { close(closed) }}, nil
	})
	_, _ = r.Tracker(seededFake()).Issue("7")
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r.Close(ctx)
	close(release)

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("a Connector that finished loading after the close deadline was never closed")
	}
}
