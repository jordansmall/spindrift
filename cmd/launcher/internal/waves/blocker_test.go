package waves

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

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
