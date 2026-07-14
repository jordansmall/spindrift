package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// TestRun_QuitCommand_ExitsCleanly verifies "q" ends the loop and returns
// without error, after rendering the initial backlog once.
func TestRun_QuitCommand_ExitsCleanly(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("q\n"), &out, nil); err != nil {
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
	if err := Run(f, t.TempDir(), strings.NewReader(""), &strings.Builder{}, nil); err != nil {
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
	if err := Run(f, t.TempDir(), in, &out, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	f.SetIssue(forge.Issue{Number: "5", Title: "late arrival", State: forge.IssueOpen})

	out.Reset()
	in = strings.NewReader("r\nq\n")
	if err := Run(f, t.TempDir(), in, &out, nil); err != nil {
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
	if err := Run(f, t.TempDir(), strings.NewReader("f b\nq\n"), &out, nil); err != nil {
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
	if err := Run(f, t.TempDir(), strings.NewReader("f b\nf\nq\n"), &out, nil); err != nil {
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
	if err := Run(f, t.TempDir(), strings.NewReader("p 42\nq\n"), &out, nil); err != nil {
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
	if err := Run(f, t.TempDir(), strings.NewReader("p 42\nu 42\nq\n"), &out, nil); err != nil {
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

// TestRun_PickCommand_WithLauncher_LaunchesRealDispatch verifies that when
// Run is given a Launcher, "p <num>" doesn't just queue the pick — it
// drives the pick through the continuous engine to settle, in the
// background, so the read loop stays responsive (#646).
func TestRun_PickCommand_WithLauncher_LaunchesRealDispatch(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})

	launch := newTestLauncher(t, f)

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("p 42\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := launch.Queue.Snapshot()
		if len(snap) == 1 && snap[0].State == PickSettled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pick never settled: %+v", snap)
		}
		time.Sleep(time.Millisecond)
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

// newTestLauncher builds a Launcher wired to a runner.Fake Box and a
// settle.Fake, matching the waves package's own dispatch.Factory test
// helper — enough plumbing to prove a picked issue runs a real (fake) Box
// and settles.
func newTestLauncher(t *testing.T, cf forge.CodeForge) *Launcher {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, runner.NewFake(), drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)
	return &Launcher{CodeForge: cf, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
}
