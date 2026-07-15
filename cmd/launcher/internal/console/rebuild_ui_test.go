package console

import (
	"strings"
	"sync/atomic"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestRun_StaleStatus_RendersBanner verifies Run's per-render sync installs
// the launcher's live stale verdict onto the view, exactly as syncQueue
// does for the picks queue — the operator sees the banner without an
// explicit refresh (issue #652 AC1).
func TestRun_StaleStatus_RendersBanner(t *testing.T) {
	f := forge.NewFake()

	launch := newTestLauncher(t, f)
	launch.Fresh = func() (bool, bool, string) { return true, false, "rebuild needed" }
	launch.tryLaunch(f, t.TempDir())
	launch.Wait()

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("q\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "stale") || !strings.Contains(out.String(), "rebuild needed") {
		t.Errorf("output = %q, want the stale banner rendered", out.String())
	}
}

// TestRun_RebuildCommand_InvokesRebuildAndResumesHeldLaunch verifies "b" (or
// "build"/"rebuild") fires Launcher.Rebuild, and a successful rebuild
// resumes a pick that had held at PickQueued through the stale window —
// launched without being re-picked (issue #652 AC3/AC4).
func TestRun_RebuildCommand_InvokesRebuildAndResumesHeldLaunch(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})

	launch := newTestLauncher(t, f)
	var stale atomic.Bool
	stale.Store(true)
	launch.Fresh = func() (bool, bool, string) {
		if stale.Load() {
			return true, false, "rebuild needed"
		}
		return true, true, ""
	}
	rebuilt := false
	launch.RebuildFn = func() error {
		rebuilt = true
		stale.Store(false)
		return nil
	}

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("p 42\nb\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !rebuilt {
		t.Fatal("RebuildFn never ran")
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickSettled {
		t.Errorf("queue pick = %+v, want settled after the rebuild resumed the held launch", snap)
	}
}
