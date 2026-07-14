package console

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestRun_QuitCommand_ExitsCleanly verifies "q" ends the loop and returns
// without error, after rendering the initial backlog once.
func TestRun_QuitCommand_ExitsCleanly(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("q\n"), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "first") {
		t.Errorf("output = %q, want the initial backlog rendered", out.String())
	}
}

// TestRun_EOF_ExitsCleanlyWithoutQuitCommand verifies Run returns once input
// runs out, even without an explicit "q" — a scripted test reader (or a
// closed pipe) must never hang the loop.
func TestRun_EOF_ExitsCleanlyWithoutQuitCommand(t *testing.T) {
	f := forge.NewFake()
	if err := Run(f, t.TempDir(), strings.NewReader(""), &strings.Builder{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRun_RefreshCommand_ReQueriesTracker verifies "r" re-queries the
// tracker and re-renders — an issue added to the tracker after Run starts
// appears only once "r" is sent.
func TestRun_RefreshCommand_ReQueriesTracker(t *testing.T) {
	f := forge.NewFake()

	var out strings.Builder
	in := strings.NewReader("r\nq\n")
	if err := Run(f, t.TempDir(), in, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	f.SetIssue(forge.Issue{Number: "5", Title: "late arrival", State: forge.IssueOpen})

	out.Reset()
	in = strings.NewReader("r\nq\n")
	if err := Run(f, t.TempDir(), in, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "late arrival") {
		t.Errorf("output after refresh = %q, want the newly added issue", out.String())
	}
}

// TestRun_FilterCommand_Narrows verifies "f <label>" renders only issues
// carrying a matching label. Run always renders the full unfiltered backlog
// first (before any command is read), so "alpha" legitimately appears once
// from that initial render — the assertion is that the *second* render,
// triggered by "f b", omits it, which a raw single-occurrence count of each
// title across the whole transcript captures directly: alpha appears only
// in the initial render (once), beta appears in both (twice).
func TestRun_FilterCommand_Narrows(t *testing.T) {
	f := newAlphaBetaFake()

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("f b\nq\n"), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Count(out.String(), "alpha"); got != 1 {
		t.Errorf("alpha occurrences = %d, want 1 (initial render only)", got)
	}
	if got := strings.Count(out.String(), "beta"); got != 2 {
		t.Errorf("beta occurrences = %d, want 2 (initial + filtered render)", got)
	}
}

// TestRun_BareFilterCommand_RestoresFullList verifies a bare "f" clears an
// active filter, restoring issues the prior filter had narrowed out: alpha
// reappears in the third render (initial, filtered-out, restored).
func TestRun_BareFilterCommand_RestoresFullList(t *testing.T) {
	f := newAlphaBetaFake()

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("f b\nf\nq\n"), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Count(out.String(), "alpha"); got != 2 {
		t.Errorf("alpha occurrences = %d, want 2 (initial + restored render)", got)
	}
}

// TestRun_PickCommand_PromotesAndQueues verifies "p <num>" promotes the
// named issue on the tracker and renders it queued — the operator's launch
// button (#646).
func TestRun_PickCommand_PromotesAndQueues(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("p 42\nq\n"), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "queued") {
		t.Errorf("output = %q, want the pick rendered queued", out.String())
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(iss, "ready-for-agent") {
		t.Errorf("issue #42 labels = %v, want ready-for-agent promoted onto it", iss.Labels)
	}
}

// TestRun_UnpickCommand_RemovesFromQueue verifies "u <num>" removes a
// queued pick and makes zero further tracker calls.
func TestRun_UnpickCommand_RemovesFromQueue(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("p 42\nu 42\nq\n"), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}

	callsAfterPick := len(f.TransitionStateCalls)
	if callsAfterPick != 1 {
		t.Fatalf("TransitionStateCalls after pick+unpick = %d, want 1 (unpick makes none)", callsAfterPick)
	}
	if strings.Count(out.String(), "queued") != 1 {
		t.Errorf("output = %q, want \"queued\" to appear only in the pick's own render, not after unpick", out.String())
	}
}

// newAlphaBetaFake returns a Fake tracker with two open issues, "alpha"
// labeled "a" and "beta" labeled "b" — shared fixture for filter tests.
func newAlphaBetaFake() *forge.Fake {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "alpha", State: forge.IssueOpen, Labels: []string{"a"}})
	f.SetIssue(forge.Issue{Number: "2", Title: "beta", State: forge.IssueOpen, Labels: []string{"b"}})
	return f
}
