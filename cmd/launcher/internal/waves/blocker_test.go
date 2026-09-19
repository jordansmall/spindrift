package waves

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
)

// concurrencyTrackingFake wraps forge.Fake's DepsOf with an in-flight counter
// and a short sleep so a test can see whether NewReadiness's DepsOf calls
// overlap. A sequential caller never pushes inFlight above 1; a concurrent one
// does regardless of GOMAXPROCS, because the sleep yields the OS thread rather
// than spinning.
type concurrencyTrackingFake struct {
	*forge.Fake
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
}

func (f *concurrencyTrackingFake) DepsOf(num string) ([]forge.Dependency, error) {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.mu.Unlock()

	time.Sleep(20 * time.Millisecond)

	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()

	return f.Fake.DepsOf(num)
}

// TestNewReadiness_DepsOfCallsOverlap guards issue #1745: NewReadiness must fan
// its per-issue DepsOf calls out concurrently, not one at a time. A sequential
// loop never observes more than one in-flight call.
func TestNewReadiness_DepsOfCallsOverlap(t *testing.T) {
	fc := &concurrencyTrackingFake{Fake: forge.NewFake()}
	fc.SetIssue(forge.Issue{Number: "1", Body: ""})
	fc.SetIssue(forge.Issue{Number: "2", Body: ""})
	fc.SetIssue(forge.Issue{Number: "3", Body: ""})

	issues := []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}
	if _, err := NewReadiness(fc, issues); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if fc.maxInFlight <= 1 {
		t.Errorf("maxInFlight = %d, want > 1 (DepsOf calls must overlap)", fc.maxInFlight)
	}
}

// TestNewReadiness_DepsOfConcurrencyBounded guards issue #1745's other half: the
// fan-out must not spawn one unbounded goroutine per issue. With more issues
// than depsOfConcurrency, maxInFlight must never exceed the cap.
func TestNewReadiness_DepsOfConcurrencyBounded(t *testing.T) {
	fc := &concurrencyTrackingFake{Fake: forge.NewFake()}
	issues := make([]Issue, depsOfConcurrency*3)
	for i := range issues {
		num := fmt.Sprintf("%d", i+1)
		fc.SetIssue(forge.Issue{Number: num, Body: ""})
		issues[i] = Issue{Number: num}
	}

	if _, err := NewReadiness(fc, issues); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if fc.maxInFlight > depsOfConcurrency {
		t.Errorf("maxInFlight = %d, want <= %d (bounded concurrency)", fc.maxInFlight, depsOfConcurrency)
	}
}

func TestNewReadiness_MultipleIssuesWithBlockers(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Body: "## Blocked by\n- #2\n- #3"})
	fc.SetIssue(forge.Issue{Number: "2", Body: "## Blocked by\n- #3"})
	fc.SetIssue(forge.Issue{Number: "3", Body: ""})

	issues := []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}
	got, err := NewReadiness(fc, issues)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	want := map[string][]string{
		"1": {"2", "3"},
		"2": {"3"},
	}
	if !reflect.DeepEqual(got.Edges, want) {
		t.Errorf("got %v, want %v", got.Edges, want)
	}
}

func TestNewReadiness_NoBlockersOmitted(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Body: "## Blocked by\n- #2"})
	fc.SetIssue(forge.Issue{Number: "2", Body: ""})

	issues := []Issue{{Number: "1"}, {Number: "2"}}
	got, err := NewReadiness(fc, issues)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if _, ok := got.Edges["2"]; ok {
		t.Errorf("issue 2 has no blockers, expected no map key, got %v", got.Edges["2"])
	}
	want := map[string][]string{"1": {"2"}}
	if !reflect.DeepEqual(got.Edges, want) {
		t.Errorf("got %v, want %v", got.Edges, want)
	}
}

func TestNewReadiness_DepsOfErrorNonFatal(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Body: "## Blocked by\n- #2"})
	// Issue "2" is deliberately not registered, so DepsOf("2") errors.
	fc.SetIssue(forge.Issue{Number: "3", Body: "## Blocked by\n- #1"})

	issues := []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}
	got, err := NewReadiness(fc, issues)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	want := map[string][]string{
		"1": {"2"},
		"3": {"1"},
	}
	if !reflect.DeepEqual(got.Edges, want) {
		t.Errorf("got %v, want %v", got.Edges, want)
	}
	if !got.Failed["2"] {
		t.Errorf("failed = %v, want it to name issue 2 (its DepsOf call errored)", got.Failed)
	}
}

// TestNewReadiness_MixedNativeAndBodySources verifies NewReadiness tags each
// blocker ref with the source DepsOf resolved it from. A native-relationship
// blocker and a body-parsed one must not collapse into the same source, or
// mixed-batch preview, skip and marker annotations cannot tell them apart.
func TestNewReadiness_MixedNativeAndBodySources(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Body: ""})
	fc.SetIssue(forge.Issue{Number: "2", Body: "## Blocked by\n- #3"})
	fc.SetIssue(forge.Issue{Number: "3", Body: ""})
	fc.NativeDeps = map[string][]string{"1": {"3"}}

	issues := []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}
	result, err := NewReadiness(fc, issues)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got := result.Sources["1"]["3"]; got != forge.DepSourceNative {
		t.Errorf("issue 1's blocker 3 source = %v, want native", got)
	}
	if got := result.Sources["2"]["3"]; got != forge.DepSourceBody {
		t.Errorf("issue 2's blocker 3 source = %v, want body", got)
	}
}

// captureStdout returns what fn printed. It restores os.Stdout on cleanup so a
// panic inside fn cannot strand the rest of the package writing to a closed
// pipe.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(captured)
}

// TestReadinessStatus_LocalTracker_HoldsAndReportsUnfetchableBlocker drives the
// body-parsed blocker path end to end through a real LocalTracker, not a
// forge.Fake stub, so a blocker slug that fails LocalTracker's path-containment
// guard (issue #3075) is hit the way a live dispatch hits it: it must come back
// unready, not silently satisfied, and the failure must show on stdout.
func TestReadinessStatus_LocalTracker_HoldsAndReportsUnfetchableBlocker(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "issues")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const traversal = "../../x"
	lt := local.NewLocalTracker(dir, forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	ref, err := lt.PostIssue("depends-on-traversal", "## Blocked by\n\n- "+traversal+"\n", nil)
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	num := strings.TrimPrefix(ref, "local:")

	c := baseConfig()
	deps, err := lt.DepsOf(num)
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	edges := map[string][]string{num: {deps[0].ID}}

	var ready bool
	var unready []string
	captured := captureStdout(t, func() {
		ready, _, unready = (Readiness{Edges: edges}).Status(c, lt, nil, capsFor(lt, nil), num)
	})

	if ready {
		t.Error("Readiness.Status: want ready=false for a traversal blocker slug, got true")
	}
	if !reflect.DeepEqual(unready, []string{traversal}) {
		t.Errorf("Readiness.Status: want unready=[%s], got %v", traversal, unready)
	}
	if !strings.Contains(captured, traversal) {
		t.Errorf("stdout must name the unreadable blocker %q, got: %s", traversal, captured)
	}
}

func TestDetectCycle_Empty(t *testing.T) {
	_, hasCycle := detectCycle(map[string][]string{}, []string{})
	if hasCycle {
		t.Error("expected no cycle in empty graph")
	}
}

func TestDetectCycle_NoCycle_Linear(t *testing.T) {
	edges := map[string][]string{
		"1": {"2"},
		"2": {"3"},
	}
	node, hasCycle := detectCycle(edges, []string{"1", "2", "3"})
	if hasCycle {
		t.Errorf("expected no cycle, got cycle member %s", node)
	}
}

func TestDetectCycle_NoCycle_Parallel(t *testing.T) {
	edges := map[string][]string{
		"1": {"3"},
		"2": {"3"},
	}
	node, hasCycle := detectCycle(edges, []string{"1", "2", "3"})
	if hasCycle {
		t.Errorf("expected no cycle, got cycle member %s", node)
	}
}

func TestDetectCycle_DirectCycle(t *testing.T) {
	edges := map[string][]string{
		"1": {"2"},
		"2": {"1"},
	}
	_, hasCycle := detectCycle(edges, []string{"1", "2"})
	if !hasCycle {
		t.Error("expected cycle, got none")
	}
}

func TestDetectCycle_TransitiveCycle(t *testing.T) {
	edges := map[string][]string{
		"1": {"2"},
		"2": {"3"},
		"3": {"1"},
	}
	_, hasCycle := detectCycle(edges, []string{"1", "2", "3"})
	if !hasCycle {
		t.Error("expected cycle, got none")
	}
}

func TestDetectCycle_ExternalBlockerIgnored(t *testing.T) {
	edges := map[string][]string{
		"1": {"99"},
	}
	node, hasCycle := detectCycle(edges, []string{"1"})
	if hasCycle {
		t.Errorf("expected no cycle (external blockers ignored in batch), got cycle member %s", node)
	}
}

func TestUnreadyBlockers_Pending(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"})
	edges := map[string][]string{"10": {"11"}}
	got := unreadyBlockers(fc, fc, capsFor(fc, fc), "10", edges, nil)
	if !reflect.DeepEqual(got, []string{"11"}) {
		t.Errorf("expected [11], got %v", got)
	}
}

func TestUnreadyBlockers_MergedAndClosedAreReady(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"})
	fc.SetPR("11", forge.PR{URL: "https://github.com/owner/repo/pull/11"})
	fc.SetPRState("https://github.com/owner/repo/pull/11", "MERGED")
	fc.SetIssue(forge.Issue{Number: "12", State: "CLOSED"})
	edges := map[string][]string{"10": {"11", "12"}}
	if got := unreadyBlockers(fc, fc, capsFor(fc, fc), "10", edges, nil); len(got) != 0 {
		t.Errorf("expected no unready blockers, got %v", got)
	}
}

func TestUnreadyBlockers_Mixed(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"})
	fc.SetPR("11", forge.PR{URL: "https://github.com/owner/repo/pull/11"})
	fc.SetPRState("https://github.com/owner/repo/pull/11", "MERGED")
	fc.SetIssue(forge.Issue{Number: "12", State: "OPEN"})
	edges := map[string][]string{"10": {"11", "12"}}
	if got := unreadyBlockers(fc, fc, capsFor(fc, fc), "10", edges, nil); !reflect.DeepEqual(got, []string{"12"}) {
		t.Errorf("expected [12], got %v", got)
	}
}

func TestReadinessReady_MergedPR(t *testing.T) {
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN"})
	fc.SetPR("agent/issue-99", forge.PR{URL: "https://github.com/owner/repo/pull/99"})
	fc.SetPRState("https://github.com/owner/repo/pull/99", forge.PRMerged)

	if !(Readiness{}).Ready(fc, fc, capsFor(fc, fc), "99", forge.SeedScope{}) {
		t.Error("Readiness.Ready: want true for merged PR, got false")
	}
}

func TestReadinessReady_OpenPRWithCompleteLabel(t *testing.T) {
	c := baseConfig()

	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN", Labels: []string{c.CompleteLabel}})
	fc.SetPR("agent/issue-99", forge.PR{URL: "https://github.com/owner/repo/pull/99"})
	// SetPR without a SetPRState override leaves the PR state OPEN.

	if (Readiness{}).Ready(fc, fc, capsFor(fc, fc), "99", forge.SeedScope{}) {
		t.Error("Readiness.Ready: want false for open PR with agent-complete label, got true")
	}
}

func TestReadinessReady_ClosedIssueFallback(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "99", State: "CLOSED"})
	// No PR registered: this is human-handled work absorbed outside spindrift.

	if !(Readiness{}).Ready(fc, fc, capsFor(fc, fc), "99", forge.SeedScope{}) {
		t.Error("Readiness.Ready: want true for closed issue with no PR, got false")
	}
}

func TestReadinessReady_LocalLandingVerifiedMerged(t *testing.T) {
	fc := forge.NewFake()
	landing := "agent/issue-99@abc123"
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN", Landing: landing})
	fc.SetLandingContained(landing, "dependent-parent", true, nil)
	scope := forge.NewSeedScope("dependent-parent", "integration/dependent-parent")

	if !(Readiness{}).Ready(fc, fc.AsLocal(), capsFor(fc, fc.AsLocal()), "99", scope) {
		t.Error("Readiness.Ready: want true for blocker whose landing is contained, got false")
	}
}

func TestReadinessReady_LocalLandingNotYetMerged(t *testing.T) {
	fc := forge.NewFake()
	landing := "agent/issue-99@abc123"
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN", Landing: landing})
	fc.SetLandingContained(landing, "dependent-parent", false, nil)
	scope := forge.NewSeedScope("dependent-parent", "integration/dependent-parent")

	if (Readiness{}).Ready(fc, fc.AsLocal(), capsFor(fc, fc.AsLocal()), "99", scope) {
		t.Error("Readiness.Ready: want false for blocker whose landing is not yet contained, got true")
	}
}

func TestReadinessReady_LocalLandingBranchRefStaysHeld(t *testing.T) {
	fc := forge.NewFake()
	// A raw, pre-merge branch ref (no "@sha") is settle's LandingBranchRef
	// shape, not yet upgraded to the containment-checkable IntegrationRef form,
	// so it must never reach LandingContained and must stay held.
	landing := "agent/issue-99"
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN", Landing: landing})
	branchLanding := forge.Landing{Kind: forge.LandingBranchRef, Branch: landing}
	fc.SetLandingContained(branchLanding.String(), "dependent-parent", true, nil)
	scope := forge.NewSeedScope("dependent-parent", "integration/dependent-parent")

	if (Readiness{}).Ready(fc, fc.AsLocal(), capsFor(fc, fc.AsLocal()), "99", scope) {
		t.Error("Readiness.Ready: want false for unmerged LandingBranchRef, got true")
	}
}

func TestReadinessReady_MergedIssueFallback(t *testing.T) {
	fc := forge.NewFake()
	// The blocker ref resolves to a PR number, so with no agent branch it falls
	// back to it.Issue(ref), which returns MERGED for a merged PR.
	fc.SetIssue(forge.Issue{Number: "99", State: "MERGED"})

	if !(Readiness{}).Ready(fc, fc, capsFor(fc, fc), "99", forge.SeedScope{}) {
		t.Error("Readiness.Ready: want true for merged issue fallback, got false")
	}
}

func TestReadinessReady_OpenIssueFallback(t *testing.T) {
	fc := forge.NewFake()
	// No PR registered, so it falls back to it.Issue(ref), which returns a
	// still-open issue that must keep blocking.
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN"})

	if (Readiness{}).Ready(fc, fc, capsFor(fc, fc), "99", forge.SeedScope{}) {
		t.Error("Readiness.Ready: want false for open issue fallback, got true")
	}
}

func TestReadinessStatus_ClosedAndFailed(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	// Closed with no PR, so Readiness.Ready's fallback treats #11 as ready, but
	// it also carries the Failed label, which must never be satisfiable.
	fc.SetIssue(forge.Issue{Number: "11", State: "CLOSED", Labels: []string{c.FailedLabel}})
	edges := map[string][]string{"10": {"11"}}

	ready, failed, unready := (Readiness{Edges: edges}).Status(c, fc, fc, capsFor(fc, fc), "10")
	if ready {
		t.Error("Readiness.Status: want ready=false for closed+failed blocker, got true")
	}
	if !reflect.DeepEqual(failed, []string{"11"}) {
		t.Errorf("Readiness.Status: want failed=[11], got %v", failed)
	}
	// Readiness.Ready's fallback already calls the closed #11 satisfied, so it
	// must stay out of unready even though it is also failed. Otherwise the
	// console renders both BlockedBy and Reason for the same blocker, the #755
	// regression Readiness.Status's doc warns about.
	if len(unready) != 0 {
		t.Errorf("Readiness.Status: want unready=[] for closed+failed blocker, got %v", unready)
	}
}

// TestReadinessStatus_OneIssueFetchPerBlocker guards the double fetch #1098
// found: unreadyBlockers' Ready call and the FailedLabel loop each called
// it.Issue(dep) for the same blocker. No PR is registered here, so Ready falls
// through to it.Issue, the path where the duplicate always fired.
func TestReadinessStatus_OneIssueFetchPerBlocker(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"})
	edges := map[string][]string{"10": {"11"}}

	(Readiness{Edges: edges}).Status(c, fc, fc, capsFor(fc, fc), "10")

	if len(fc.IssueCalls) != 1 {
		t.Errorf("IssueCalls = %v, want exactly 1 (no duplicate fetch)", fc.IssueCalls)
	}
}

// TestReadinessStatus_MergedPRStillChecksFailedLabel covers the fi == nil
// branch: a merged PR resolves readiness without blockerReady calling it.Issue,
// so the FailedLabel loop's fetch is the only call, not a duplicate. It must
// still run, or a failed-labeled blocker with a stale merged PR slips past the
// failed check.
func TestReadinessStatus_MergedPRStillChecksFailedLabel(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN", Labels: []string{c.FailedLabel}})
	fc.SetPR("agent/issue-11", forge.PR{URL: "https://github.com/owner/repo/pull/11"})
	fc.SetPRState("https://github.com/owner/repo/pull/11", forge.PRMerged)
	edges := map[string][]string{"10": {"11"}}

	ready, failed, unready := (Readiness{Edges: edges}).Status(c, fc, fc, capsFor(fc, fc), "10")

	if ready {
		t.Error("Readiness.Status: want ready=false for merged PR with Failed label, got true")
	}
	if !reflect.DeepEqual(failed, []string{"11"}) {
		t.Errorf("Readiness.Status: want failed=[11], got %v", failed)
	}
	if len(unready) != 0 {
		t.Errorf("Readiness.Status: want unready=[] (merged PR is ready), got %v", unready)
	}
	if len(fc.IssueCalls) != 1 {
		t.Errorf("IssueCalls = %v, want exactly 1 (merged-PR path fetches once for FailedLabel)", fc.IssueCalls)
	}
}

// TestReadinessStatus_MultipleBlockersOneFetchEach extends the one-fetch
// invariant across a mixed set of blockers (push-only-style fall-through and
// merged PR) so the dedup holds per dep, not just for a single blocker.
func TestReadinessStatus_MultipleBlockersOneFetchEach(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"})
	fc.SetIssue(forge.Issue{Number: "12", State: "CLOSED"})
	fc.SetPR("agent/issue-12", forge.PR{URL: "https://github.com/owner/repo/pull/12"})
	fc.SetPRState("https://github.com/owner/repo/pull/12", forge.PRMerged)
	edges := map[string][]string{"10": {"11", "12"}}

	(Readiness{Edges: edges}).Status(c, fc, fc, capsFor(fc, fc), "10")

	if len(fc.IssueCalls) != 2 {
		t.Errorf("IssueCalls = %v, want exactly 2 (one per blocker)", fc.IssueCalls)
	}
}

// TestBlockerStatus_SeedBranchGate_NotContainedOnDependentParentHolds guards the
// #2130 dependent seed-branch gate: a blocker whose landing has not reached the
// dependent's own integration/<parent> seed branch stays unready even though the
// blocker issue is still open. The gate must check the dependent's own seed
// branch, not the blocker's.
func TestBlockerStatus_SeedBranchGate_NotContainedOnDependentParentHolds(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	landing := "integration/999@shaXYZ"
	fc.SetIssue(forge.Issue{Number: "12", State: "OPEN", Landing: landing})
	fc.SetLandingContained(landing, "dependent-parent", false, nil)
	cf := fc.AsLocal()
	cfg := c
	cfg.SeedScopeOf = func(string) forge.SeedScope {
		return forge.NewSeedScope("dependent-parent", "integration/dependent-parent")
	}
	edges := map[string][]string{"10": {"12"}}

	ready, _, unready := (Readiness{Edges: edges}).Status(cfg, fc, cf, capsFor(fc, cf), "10")

	if ready {
		t.Error("Status: want ready=false when blocker's landing has not reached the dependent's seed branch, got true")
	}
	if !reflect.DeepEqual(unready, []string{"12"}) {
		t.Errorf("Status: want unready=[12], got %v", unready)
	}
	// The blocker issue is still open with no PR, so the seed-branch containment
	// check, not a closed or merged fallback, must be what held it.
	found := false
	for _, n := range fc.IssueCalls {
		if n == "12" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected blocker #12 to be fetched at least once, got %v", fc.IssueCalls)
	}
}

// TestBlockerStatus_SeedBranchGate_ContainmentErrorHolds verifies that when the
// local Code Forge's LandingContained call errors (a git merge-base failure,
// say), the dependent stays unready rather than treating the blocker as
// satisfied.
func TestBlockerStatus_SeedBranchGate_ContainmentErrorHolds(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	landing := "integration/999@shaXYZ"
	fc.SetIssue(forge.Issue{Number: "12", State: "OPEN", Landing: landing})
	fc.SetLandingContained(landing, "dependent-parent", false, errors.New("git merge-base exploded"))
	cf := fc.AsLocal()
	cfg := c
	cfg.SeedScopeOf = func(string) forge.SeedScope {
		return forge.NewSeedScope("dependent-parent", "integration/dependent-parent")
	}
	edges := map[string][]string{"10": {"12"}}

	ready, _, unready := (Readiness{Edges: edges}).Status(cfg, fc, cf, capsFor(fc, cf), "10")

	if ready {
		t.Error("Status: want ready=false when the seed-branch containment check errors, got true")
	}
	if !reflect.DeepEqual(unready, []string{"12"}) {
		t.Errorf("Status: want unready=[12], got %v", unready)
	}
}

// TestBlockerStatus_SeedBranchGate_ContainedOnDependentParentReady verifies the
// gate satisfies a blocker whose landing HAS reached the dependent's own
// integration/<parent> seed branch.
func TestBlockerStatus_SeedBranchGate_ContainedOnDependentParentReady(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	landing := "integration/999@shaXYZ"
	fc.SetIssue(forge.Issue{Number: "12", State: "OPEN", Landing: landing})
	fc.SetLandingContained(landing, "dependent-parent", true, nil)
	cf := fc.AsLocal()
	cfg := c
	cfg.SeedScopeOf = func(string) forge.SeedScope {
		return forge.NewSeedScope("dependent-parent", "integration/dependent-parent")
	}
	edges := map[string][]string{"10": {"12"}}

	ready, _, unready := (Readiness{Edges: edges}).Status(cfg, fc, cf, capsFor(fc, cf), "10")

	if !ready {
		t.Error("Status: want ready=true when blocker's landing is contained in the dependent's seed branch, got false")
	}
	if len(unready) != 0 {
		t.Errorf("Status: want unready=[], got %v", unready)
	}
}

// TestBlockerStatus_SeedBranchGate_ClosedBlockerReadyRegardlessOfContainment
// verifies a closed blocker still satisfies the gate regardless of seed-branch
// containment: the IssueClosed fallback runs before the containment check, and
// the containment check must not short-circuit it.
func TestBlockerStatus_SeedBranchGate_ClosedBlockerReadyRegardlessOfContainment(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	landing := "integration/999@shaXYZ"
	fc.SetIssue(forge.Issue{Number: "12", State: "CLOSED", Landing: landing})
	fc.SetLandingContained(landing, "dependent-parent", false, nil)
	cf := fc.AsLocal()
	cfg := c
	cfg.SeedScopeOf = func(string) forge.SeedScope {
		return forge.NewSeedScope("dependent-parent", "integration/dependent-parent")
	}
	edges := map[string][]string{"10": {"12"}}

	ready, _, unready := (Readiness{Edges: edges}).Status(cfg, fc, cf, capsFor(fc, cf), "10")

	if !ready {
		t.Error("Status: want ready=true for a closed blocker regardless of seed-branch containment, got false")
	}
	if len(unready) != 0 {
		t.Errorf("Status: want unready=[], got %v", unready)
	}
}

// TestReadinessReady_SeedBranchGate_EmptySeedScopeNeverChecksContainment
// verifies a zero SeedScope (nil SeedScopeOf) skips the containment check
// entirely, since issue #2151 dropped the pre-#2130 no-scope self-verification
// fallback. An open IntegrationRef-landed blocker stays unready even when
// LandingContained is scripted true, because blockerReady never calls it.
func TestReadinessReady_SeedBranchGate_EmptySeedScopeNeverChecksContainment(t *testing.T) {
	fc := forge.NewFake()
	landing := "agent/issue-99@abc123"
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN", Landing: landing})
	fc.SetLandingContained(landing, "", true, nil)

	if (Readiness{}).Ready(fc, fc.AsLocal(), capsFor(fc, fc.AsLocal()), "99", forge.SeedScope{}) {
		t.Error("Ready: want false for an open IntegrationRef-landed blocker with an empty SeedScope, got true")
	}
}
