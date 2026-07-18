package waves

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// --- BuildEdges tests ---

func TestBuildEdges_MultipleIssuesWithBlockers(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Body: "## Blocked by\n- #2\n- #3"})
	fc.SetIssue(forge.Issue{Number: "2", Body: "## Blocked by\n- #3"})
	fc.SetIssue(forge.Issue{Number: "3", Body: ""})

	issues := []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}
	got, _, _, err := BuildEdges(fc, issues)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	want := map[string][]string{
		"1": {"2", "3"},
		"2": {"3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBuildEdges_NoBlockersOmitted(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Body: "## Blocked by\n- #2"})
	fc.SetIssue(forge.Issue{Number: "2", Body: ""})

	issues := []Issue{{Number: "1"}, {Number: "2"}}
	got, _, _, err := BuildEdges(fc, issues)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if _, ok := got["2"]; ok {
		t.Errorf("issue 2 has no blockers, expected no map key, got %v", got["2"])
	}
	want := map[string][]string{"1": {"2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBuildEdges_DepsOfErrorNonFatal(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Body: "## Blocked by\n- #2"})
	// Issue "2" is deliberately not registered, so DepsOf("2") errors.
	fc.SetIssue(forge.Issue{Number: "3", Body: "## Blocked by\n- #1"})

	issues := []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}
	got, _, failed, err := BuildEdges(fc, issues)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	want := map[string][]string{
		"1": {"2"},
		"3": {"1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !failed["2"] {
		t.Errorf("failed = %v, want it to name issue 2 (its DepsOf call errored)", failed)
	}
}

// TestBuildEdges_MixedNativeAndBodySources verifies BuildEdges tags each
// blocker ref with the source DepsOf resolved it from — one issue's
// native-relationship blocker and another's body-parsed blocker must not
// collapse into the same source, so mixed-batch preview/skip/marker
// annotations can tell them apart.
func TestBuildEdges_MixedNativeAndBodySources(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Body: ""})
	fc.SetIssue(forge.Issue{Number: "2", Body: "## Blocked by\n- #3"})
	fc.SetIssue(forge.Issue{Number: "3", Body: ""})
	fc.NativeDeps = map[string][]string{"1": {"3"}}

	issues := []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}
	_, sources, _, err := BuildEdges(fc, issues)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got := sources["1"]["3"]; got != forge.DepSourceNative {
		t.Errorf("issue 1's blocker 3 source = %v, want native", got)
	}
	if got := sources["2"]["3"]; got != forge.DepSourceBody {
		t.Errorf("issue 2's blocker 3 source = %v, want body", got)
	}
}

// --- detectCycle tests ---

func TestDetectCycle_Empty(t *testing.T) {
	_, hasCycle := detectCycle(map[string][]string{}, []string{})
	if hasCycle {
		t.Error("expected no cycle in empty graph")
	}
}

func TestDetectCycle_NoCycle_Linear(t *testing.T) {
	// 1 depends on 2, 2 depends on 3 (1→2→3)
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
	// 1 and 2 both depend on 3 (independent blockers)
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
	// 1 depends on 2 and 2 depends on 1
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
	// 1→2→3→1
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
	// 1 depends on 99 (external, not in batch)
	edges := map[string][]string{
		"1": {"99"},
	}
	node, hasCycle := detectCycle(edges, []string{"1"})
	if hasCycle {
		t.Errorf("expected no cycle (external blockers ignored in batch), got cycle member %s", node)
	}
}

// --- unreadyBlockers tests ---

func TestUnreadyBlockers_Pending(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"}) // no complete label, still open
	edges := map[string][]string{"10": {"11"}}
	got := unreadyBlockers(fc, fc, "10", edges)
	if !reflect.DeepEqual(got, []string{"11"}) {
		t.Errorf("expected [11], got %v", got)
	}
}

func TestUnreadyBlockers_MergedAndClosedAreReady(t *testing.T) {
	fc := forge.NewFake()
	// #11: PR merged — satisfied by merged PR regardless of labels.
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"})
	fc.SetPR("11", forge.PR{URL: "https://github.com/owner/repo/pull/11"})
	fc.SetPRState("https://github.com/owner/repo/pull/11", "MERGED")
	// #12: issue closed with no PR — fallback satisfied.
	fc.SetIssue(forge.Issue{Number: "12", State: "CLOSED"})
	edges := map[string][]string{"10": {"11", "12"}}
	if got := unreadyBlockers(fc, fc, "10", edges); len(got) != 0 {
		t.Errorf("expected no unready blockers, got %v", got)
	}
}

func TestUnreadyBlockers_Mixed(t *testing.T) {
	fc := forge.NewFake()
	// #11: PR merged — satisfied.
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"})
	fc.SetPR("11", forge.PR{URL: "https://github.com/owner/repo/pull/11"})
	fc.SetPRState("https://github.com/owner/repo/pull/11", "MERGED")
	// #12: still open with no merged PR — blocking.
	fc.SetIssue(forge.Issue{Number: "12", State: "OPEN"})
	edges := map[string][]string{"10": {"11", "12"}}
	if got := unreadyBlockers(fc, fc, "10", edges); !reflect.DeepEqual(got, []string{"12"}) {
		t.Errorf("expected [12], got %v", got)
	}
}

func TestBlockerReady_MergedPR(t *testing.T) {
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN"})
	fc.SetPR("agent/issue-99", forge.PR{URL: "https://github.com/owner/repo/pull/99"})
	fc.SetPRState("https://github.com/owner/repo/pull/99", forge.PRMerged)

	if !BlockerReady(fc, fc, "99") {
		t.Error("blockerReady: want true for merged PR, got false")
	}
}

func TestBlockerReady_OpenPRWithCompleteLabel(t *testing.T) {
	c := baseConfig()

	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN", Labels: []string{c.CompleteLabel}})
	fc.SetPR("agent/issue-99", forge.PR{URL: "https://github.com/owner/repo/pull/99"})
	// state defaults to OPEN when SetPR is called without SetPRState override

	if BlockerReady(fc, fc, "99") {
		t.Error("blockerReady: want false for open PR with agent-complete label, got true")
	}
}

func TestBlockerReady_ClosedIssueFallback(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "99", State: "CLOSED"})
	// No PR registered — simulates human-handled work absorbed outside spindrift.

	if !BlockerReady(fc, fc, "99") {
		t.Error("blockerReady: want true for closed issue with no PR, got false")
	}
}

// --- BlockerStatus tests ---

func TestBlockerStatus_ClosedAndFailed(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	// #11: closed with no PR — BlockerReady's fallback treats it as ready,
	// but it also carries the Failed label, which must never be satisfiable.
	fc.SetIssue(forge.Issue{Number: "11", State: "CLOSED", Labels: []string{c.FailedLabel}})
	edges := map[string][]string{"10": {"11"}}

	ready, failed, unready := BlockerStatus(c, fc, fc, "10", edges)
	if ready {
		t.Error("BlockerStatus: want ready=false for closed+failed blocker, got true")
	}
	if !reflect.DeepEqual(failed, []string{"11"}) {
		t.Errorf("BlockerStatus: want failed=[11], got %v", failed)
	}
	// #11 is closed, so BlockerReady's fallback (blocker.go) already calls it
	// satisfied — it must stay out of unready even though it's also failed,
	// or the console would redundantly render both BlockedBy and Reason for
	// the same blocker (the #755 regression the BlockerStatus doc warns about).
	if len(unready) != 0 {
		t.Errorf("BlockerStatus: want unready=[] for closed+failed blocker, got %v", unready)
	}
}

// TestBlockerStatus_OneIssueFetchPerBlocker guards against the double-fetch
// #1098 found: unreadyBlockers' BlockerReady call and the FailedLabel loop
// each independently called it.Issue(dep) for the same blocker. No PR is
// registered here, so BlockerReady falls through to it.Issue — the path
// where the duplicate always fired.
func TestBlockerStatus_OneIssueFetchPerBlocker(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"})
	edges := map[string][]string{"10": {"11"}}

	BlockerStatus(c, fc, fc, "10", edges)

	if len(fc.IssueCalls) != 1 {
		t.Errorf("IssueCalls = %v, want exactly 1 (no duplicate fetch)", fc.IssueCalls)
	}
}

// TestBlockerStatus_MergedPRStillChecksFailedLabel covers the fi == nil
// branch: a merged PR resolves readiness without blockerReady ever calling
// it.Issue, so the FailedLabel loop's fetch is the only call, not a
// duplicate — and it must still run so a failed-labeled blocker with a
// stale merged PR can't slip past the failed check.
func TestBlockerStatus_MergedPRStillChecksFailedLabel(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN", Labels: []string{c.FailedLabel}})
	fc.SetPR("agent/issue-11", forge.PR{URL: "https://github.com/owner/repo/pull/11"})
	fc.SetPRState("https://github.com/owner/repo/pull/11", forge.PRMerged)
	edges := map[string][]string{"10": {"11"}}

	ready, failed, unready := BlockerStatus(c, fc, fc, "10", edges)

	if ready {
		t.Error("BlockerStatus: want ready=false for merged PR with Failed label, got true")
	}
	if !reflect.DeepEqual(failed, []string{"11"}) {
		t.Errorf("BlockerStatus: want failed=[11], got %v", failed)
	}
	if len(unready) != 0 {
		t.Errorf("BlockerStatus: want unready=[] (merged PR is ready), got %v", unready)
	}
	if len(fc.IssueCalls) != 1 {
		t.Errorf("IssueCalls = %v, want exactly 1 (merged-PR path fetches once for FailedLabel)", fc.IssueCalls)
	}
}

// TestBlockerStatus_MultipleBlockersOneFetchEach extends the one-fetch
// invariant across a mixed set of blockers (push-only-style fall-through and
// merged-PR) so the dedup holds per-dep, not just for a single blocker.
func TestBlockerStatus_MultipleBlockersOneFetchEach(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"})
	fc.SetIssue(forge.Issue{Number: "12", State: "CLOSED"})
	fc.SetPR("agent/issue-12", forge.PR{URL: "https://github.com/owner/repo/pull/12"})
	fc.SetPRState("https://github.com/owner/repo/pull/12", forge.PRMerged)
	edges := map[string][]string{"10": {"11", "12"}}

	BlockerStatus(c, fc, fc, "10", edges)

	if len(fc.IssueCalls) != 2 {
		t.Errorf("IssueCalls = %v, want exactly 2 (one per blocker)", fc.IssueCalls)
	}
}
