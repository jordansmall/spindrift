// Package forgetest is the executable contract for forge.IssueTracker: every
// adapter (github, forgejo, jira, local, and the shared Fake) runs this one
// suite against its own scripted-backend harness, so drift between the Fake
// and a real adapter fails CI.
package forgetest

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// Harness lets RunTrackerContract drive an IssueTracker's scripted backend
// without knowing which adapter it is.
type Harness interface {
	Tracker() forge.IssueTracker
	SeedIssue(forge.Issue)
	// FailNativeDeps scripts a native-API error for issue num's next DepsOf
	// call, on harnesses that implement NativeFailureIsolatable.
	FailNativeDeps(num string)
}

// NativeCapable is implemented by harnesses whose backend has a native
// dependency-relationship concept distinct from body-text parsing, so the
// native-wins scenario applies to them. The local adapter has none.
type NativeCapable interface {
	// SeedNativeDeps registers ids as num's native dependencies, independent
	// of whatever body text SeedIssue wrote.
	SeedNativeDeps(num string, ids []string)
}

// NativeFailureIsolatable is implemented by harnesses where a native lookup
// failure can be exercised independent of body-content availability (issue
// #1544 AC2). Jira's native lookup and its Issue body fetch share one request,
// so the two cannot be decoupled; local has no native concept to fail.
type NativeFailureIsolatable interface {
	IsolatesNativeFailure()
}

// PriorityCapable is implemented by harnesses whose adapter resolves the
// agent-priority-* label family (ADR 0040) into a forge.Priority tier: github
// and the Fake (#2281), forgejo (#2283). jira and local default every issue to
// PriorityNormal permanently, so testLabelToPriority asserts that instead.
type PriorityCapable interface {
	IsPriorityCapable()
}

// RunTrackerContract runs the shared IssueTracker conformance suite against h,
// backed by the calling adapter's own scripted-backend Harness.
func RunTrackerContract(t *testing.T, h Harness) {
	t.Run("DispatchLifecycle", func(t *testing.T) { testDispatchLifecycle(t, h) })
	t.Run("DoubleDispatchGuard", func(t *testing.T) { testDoubleDispatchGuard(t, h) })
	t.Run("DepsOf", func(t *testing.T) { testDepsOf(t, h) })
	t.Run("ResearchVerdictTerminals", func(t *testing.T) { testResearchVerdictTerminals(t, h) })
	t.Run("DispatchOrder", func(t *testing.T) { testDispatchOrder(t, h) })
	t.Run("LabelToPriority", func(t *testing.T) { testLabelToPriority(t, h) })
}

// testDispatchLifecycle checks that ListIssues(state) reflects the current
// state after each TransitionState, never a stale one.
func testDispatchLifecycle(t *testing.T, h Harness) {
	tr := h.Tracker()
	h.SeedIssue(forge.Issue{Number: "101", Title: "lifecycle"})

	if err := tr.TransitionState("101", forge.Untriaged, forge.Dispatchable); err != nil {
		t.Fatalf("TransitionState(Untriaged, Dispatchable): %v", err)
	}
	requireIn(t, tr, forge.Dispatchable, "101")
	requireNotIn(t, tr, forge.InProgress, "101")

	if err := tr.TransitionState("101", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState(Dispatchable, InProgress): %v", err)
	}
	requireIn(t, tr, forge.InProgress, "101")
	requireNotIn(t, tr, forge.Dispatchable, "101")

	if err := tr.TransitionState("101", forge.InProgress, forge.Complete); err != nil {
		t.Fatalf("TransitionState(InProgress, Complete): %v", err)
	}
	requireIn(t, tr, forge.Complete, "101")
	requireNotIn(t, tr, forge.InProgress, "101")

	h.SeedIssue(forge.Issue{Number: "102", Title: "failed path"})
	if err := tr.TransitionState("102", forge.Untriaged, forge.Dispatchable); err != nil {
		t.Fatalf("TransitionState(Untriaged, Dispatchable): %v", err)
	}
	if err := tr.TransitionState("102", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState(Dispatchable, InProgress): %v", err)
	}
	if err := tr.TransitionState("102", forge.InProgress, forge.Failed); err != nil {
		t.Fatalf("TransitionState(InProgress, Failed): %v", err)
	}
	requireIn(t, tr, forge.Failed, "102")
	requireNotIn(t, tr, forge.InProgress, "102")
}

// testDoubleDispatchGuard checks that CompleteVerdict requires InProgress: it
// succeeds once, then errors on a second call without changing the terminal
// label already landed (#701).
func testDoubleDispatchGuard(t *testing.T, h Harness) {
	tr := h.Tracker()
	h.SeedIssue(forge.Issue{Number: "201", Title: "double dispatch"})
	if err := tr.TransitionState("201", forge.Untriaged, forge.Dispatchable); err != nil {
		t.Fatalf("TransitionState(Untriaged, Dispatchable): %v", err)
	}
	if err := tr.TransitionState("201", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState(Dispatchable, InProgress): %v", err)
	}

	if err := tr.CompleteVerdict("201", forge.Recommend); err != nil {
		t.Fatalf("first CompleteVerdict: %v", err)
	}

	if err := tr.CompleteVerdict("201", forge.Reject); err == nil {
		t.Fatal("second CompleteVerdict on an issue that already left InProgress: want error, got nil")
	}
}

// testDepsOf checks DepsOf's native-wins-when-non-empty rule and, where the
// harness can decouple native failure from body-content availability, the
// fallback to body (issue #1544 AC2).
func testDepsOf(t *testing.T, h Harness) {
	nc, hasNative := h.(NativeCapable)

	if !hasNative {
		// local has no native concept and uses its own body grammar: a
		// slug-bullet "## Blocked by" section, not github/Fake's inline
		// "blocked by #N" prose.
		h.SeedIssue(forge.Issue{Number: "301", Body: "## Blocked by\n- 7\n"})
		deps, err := h.Tracker().DepsOf("301")
		if err != nil {
			t.Fatalf("DepsOf: %v", err)
		}
		want := []forge.Dependency{{ID: "7", Source: forge.DepSourceBody}}
		if !equalDeps(deps, want) {
			t.Fatalf("DepsOf = %v, want %v", deps, want)
		}
		return
	}

	t.Run("NativeWins", func(t *testing.T) {
		h.SeedIssue(forge.Issue{Number: "302", Body: "Blocked by #7"})
		nc.SeedNativeDeps("302", []string{"999"})
		deps, err := h.Tracker().DepsOf("302")
		if err != nil {
			t.Fatalf("DepsOf: %v", err)
		}
		want := []forge.Dependency{{ID: "999", Source: forge.DepSourceNative}}
		if !equalDeps(deps, want) {
			t.Fatalf("DepsOf = %v, want %v (native must win over body, never merge)", deps, want)
		}
	})

	if _, isolatable := h.(NativeFailureIsolatable); isolatable {
		t.Run("NativeErrorFallsBackToBody", func(t *testing.T) {
			h.SeedIssue(forge.Issue{Number: "303", Body: "Blocked by #7"})
			nc.SeedNativeDeps("303", []string{"999"})
			h.FailNativeDeps("303")
			deps, err := h.Tracker().DepsOf("303")
			if err != nil {
				t.Fatalf("DepsOf: %v", err)
			}
			want := []forge.Dependency{{ID: "7", Source: forge.DepSourceBody}}
			if !equalDeps(deps, want) {
				t.Fatalf("DepsOf = %v, want %v (native error must fall back to body)", deps, want)
			}
		})
	}
}

// testResearchVerdictTerminals checks CompleteVerdict lands each research
// verdict terminal (ADR 0022) and clears InProgress. The harness's Tracker
// must be constructed with forge.ResearchVerdictLabels() for the expected
// label strings to hold.
func testResearchVerdictTerminals(t *testing.T, h Harness) {
	verdictLabels := forge.ResearchVerdictLabels()
	cases := []struct {
		num       string
		verdict   forge.Verdict
		wantLabel string
	}{
		{"401", forge.Recommend, verdictLabels.Label(forge.Recommend)},
		{"402", forge.Reject, verdictLabels.Label(forge.Reject)},
		{"403", forge.Unclear, verdictLabels.Label(forge.Unclear)},
	}
	tr := h.Tracker()
	for _, tc := range cases {
		h.SeedIssue(forge.Issue{Number: tc.num, Title: "research verdict"})
		if err := tr.TransitionState(tc.num, forge.Untriaged, forge.Dispatchable); err != nil {
			t.Fatalf("%s: TransitionState(Untriaged, Dispatchable): %v", tc.num, err)
		}
		if err := tr.TransitionState(tc.num, forge.Dispatchable, forge.InProgress); err != nil {
			t.Fatalf("%s: TransitionState(Dispatchable, InProgress): %v", tc.num, err)
		}
		if err := tr.CompleteVerdict(tc.num, tc.verdict); err != nil {
			t.Fatalf("%s: CompleteVerdict(%v): %v", tc.num, tc.verdict, err)
		}
		requireNotIn(t, tr, forge.InProgress, tc.num)

		iss, err := tr.Issue(tc.num)
		if err != nil {
			t.Fatalf("%s: Issue: %v", tc.num, err)
		}
		found := false
		for _, l := range iss.Labels {
			if l == tc.wantLabel {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s: labels = %v, want to contain verdict terminal %q", tc.num, iss.Labels, tc.wantLabel)
		}
	}
}

// testDispatchOrder checks ListIssues returns issues in each adapter's
// canonical order. Seeding in ascending-number order makes the assertion hold
// whether the adapter orders by issue number or by creation time, which
// matches insertion order in a scripted backend.
func testDispatchOrder(t *testing.T, h Harness) {
	tr := h.Tracker()
	want := []string{"501", "502", "503"}
	for _, num := range want {
		h.SeedIssue(forge.Issue{Number: num, Title: "order"})
		if err := tr.TransitionState(num, forge.Untriaged, forge.Dispatchable); err != nil {
			t.Fatalf("%s: TransitionState(Untriaged, Dispatchable): %v", num, err)
		}
	}

	issues, err := tr.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues(Dispatchable): %v", err)
	}
	var got []string
	inWant := map[string]bool{"501": true, "502": true, "503": true}
	for _, iss := range issues {
		if inWant[iss.Number] {
			got = append(got, iss.Number)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("ListIssues(Dispatchable) contains %v of our seeded issues, want all of %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListIssues(Dispatchable) order = %v, want %v", got, want)
		}
	}
}

// testLabelToPriority checks label-to-Priority resolution (ADR 0040). An
// unlabeled issue resolves to PriorityNormal on every adapter. A
// PriorityCapable harness resolves each agent-priority-* label to its tier and
// a conflicting pair to the highest; jira and local stay at PriorityNormal
// even when an issue carries one of those labels.
func testLabelToPriority(t *testing.T, h Harness) {
	tr := h.Tracker()
	_, capable := h.(PriorityCapable)

	h.SeedIssue(forge.Issue{Number: "601", Title: "unlabeled"})
	requirePriority(t, tr, "601", forge.PriorityNormal)

	cases := []struct {
		num    string
		labels []string
		want   forge.Priority
	}{
		{"602", []string{"agent-priority-critical"}, forge.PriorityCritical},
		{"603", []string{"agent-priority-high"}, forge.PriorityHigh},
		{"604", []string{"agent-priority-low"}, forge.PriorityLow},
		{"605", []string{"agent-priority-low", "agent-priority-critical"}, forge.PriorityCritical},
	}
	for _, tc := range cases {
		h.SeedIssue(forge.Issue{Number: tc.num, Title: "labeled", Labels: tc.labels})
		want := forge.PriorityNormal
		if capable {
			want = tc.want
		}
		requirePriority(t, tr, tc.num, want)
	}
}

func requirePriority(t *testing.T, tr forge.IssueTracker, num string, want forge.Priority) {
	t.Helper()
	iss, err := tr.Issue(num)
	if err != nil {
		t.Fatalf("Issue(%s): %v", num, err)
	}
	if iss.Priority != want {
		t.Fatalf("Issue(%s).Priority = %v, want %v", num, iss.Priority, want)
	}
}

func equalDeps(a, b []forge.Dependency) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func requireIn(t *testing.T, tr forge.IssueTracker, state forge.DispatchState, num string) {
	t.Helper()
	issues, err := tr.ListIssues(state)
	if err != nil {
		t.Fatalf("ListIssues(%v): %v", state, err)
	}
	for _, iss := range issues {
		if iss.Number == num {
			return
		}
	}
	t.Fatalf("ListIssues(%v) = %v, want it to contain %q", state, numbers(issues), num)
}

func requireNotIn(t *testing.T, tr forge.IssueTracker, state forge.DispatchState, num string) {
	t.Helper()
	issues, err := tr.ListIssues(state)
	if err != nil {
		t.Fatalf("ListIssues(%v): %v", state, err)
	}
	for _, iss := range issues {
		if iss.Number == num {
			t.Fatalf("ListIssues(%v) = %v, want it to NOT contain %q", state, numbers(issues), num)
		}
	}
}

func numbers(issues []forge.Issue) []string {
	out := make([]string, len(issues))
	for i, iss := range issues {
		out[i] = iss.Number
	}
	return out
}
