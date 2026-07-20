// Package forgetest is the executable contract for forge.IssueTracker: one
// shared test suite every adapter — github, jira, local, and the shared
// Fake — runs against its own scripted-backend harness, so semantic drift
// between the Fake and a real adapter fails CI instead of resting on
// "mirrors the real adapter" comments and reviewer discipline.
package forgetest

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// Harness lets RunTrackerContract drive an IssueTracker's scripted backend
// without knowing which adapter it is.
type Harness interface {
	// Tracker returns the IssueTracker under test.
	Tracker() forge.IssueTracker
	// SeedIssue puts an issue in the scripted backend.
	SeedIssue(forge.Issue)
	// FailNativeDeps scripts a native-API error for issue num's next DepsOf
	// call, on harnesses that implement NativeFailureIsolatable.
	FailNativeDeps(num string)
}

// NativeCapable is implemented by harnesses whose backend has a genuine
// native dependency-relationship concept distinct from body-text parsing
// (github, jira, and the Fake once NativeDeps is seeded) — RunTrackerContract
// type-asserts for it to decide whether the native-wins scenario applies.
// The local adapter has no native concept and does not implement it.
type NativeCapable interface {
	// SeedNativeDeps registers ids as num's native dependency relationships,
	// independent of whatever body text SeedIssue wrote.
	SeedNativeDeps(num string, ids []string)
}

// NativeFailureIsolatable is implemented by harnesses where a native lookup
// failure can be exercised independent of body-content availability — the
// Fake and the github adapter only (issue #1544 AC2). Jira's native lookup
// and its Issue body fetch share one underlying request, so the two can't be
// decoupled; local has no native concept to fail.
type NativeFailureIsolatable interface {
	IsolatesNativeFailure()
}

// RunTrackerContract runs the shared IssueTracker conformance suite against
// h. Every adapter package calls this from its own test file, backed by its
// own scripted-backend Harness.
func RunTrackerContract(t *testing.T, h Harness) {
	t.Run("DispatchLifecycle", func(t *testing.T) { testDispatchLifecycle(t, h) })
	t.Run("DoubleDispatchGuard", func(t *testing.T) { testDoubleDispatchGuard(t, h) })
	t.Run("DepsOf", func(t *testing.T) { testDepsOf(t, h) })
	t.Run("ResearchVerdictTerminals", func(t *testing.T) { testResearchVerdictTerminals(t, h) })
	t.Run("DispatchOrder", func(t *testing.T) { testDispatchOrder(t, h) })
}

// testDispatchLifecycle verifies TransitionState's label/state swaps move an
// issue through the full work-kind lifecycle — Untriaged through each
// terminal — and that ListIssues(state) reflects exactly the current state
// at each step, never a stale one.
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

// testDoubleDispatchGuard verifies CompleteVerdict asserts the issue still
// carries InProgress before swapping in a verdict label: it must succeed the
// first time (InProgress present), then error — without changing the
// terminal label already landed — on a second call for the same issue,
// which no longer carries InProgress (#701, mirrored in Fake by d07bfb0).
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

// testDepsOf verifies DepsOf's native-wins-when-non-empty rule and, where
// the harness's backend can decouple native failure from body-content
// availability, the native-error-falls-back-to-body path (issue #1544 AC2:
// exercised on the Fake and github only — jira's native lookup and Issue
// fetch share one request, and local has no native concept at all, so
// neither harness implements NativeFailureIsolatable).
func testDepsOf(t *testing.T, h Harness) {
	nc, hasNative := h.(NativeCapable)

	if !hasNative {
		// Non-native-concept adapters (currently only local) use their own
		// body grammar for blocker refs — local's slug-bullet "## Blocked
		// by" section, not github/Fake's inline "blocked by #N" prose.
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

// testResearchVerdictTerminals verifies CompleteVerdict lands each of the
// three research verdict terminals (ADR 0022) and clears InProgress — the
// harness's Tracker must be constructed with forge.ResearchVerdictLabels()
// (fixed, not operator-configurable per verdict.go) so the expected label
// strings below hold for every adapter.
func testResearchVerdictTerminals(t *testing.T, h Harness) {
	verdictLabels := forge.ResearchVerdictLabels()
	cases := []struct {
		num       string
		verdict   forge.Verdict
		wantLabel string
	}{
		{"401", forge.Recommend, verdictLabels.Recommend},
		{"402", forge.Reject, verdictLabels.Reject},
		{"403", forge.Unclear, verdictLabels.Unclear},
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

// testDispatchOrder verifies ListIssues returns issues in each adapter's
// canonical order — seeded and moved to Dispatchable in ascending-number
// order, so the assertion holds regardless of whether an adapter's native
// order key is issue number (github, Fake) or creation time (local, jira),
// which naturally coincides with insertion order in a scripted-backend
// harness.
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

// requireIn fails the test unless num appears in ListIssues(state).
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

// requireNotIn fails the test if num appears in ListIssues(state).
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
