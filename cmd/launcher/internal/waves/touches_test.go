package waves

import (
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

// TestTouchSetsOverlap_LiteralPathHit verifies touchSetsOverlap reports an
// overlap when any pattern in a matches any pattern in b, delegating the
// per-pair glob semantics (pinned in internal/glob) to glob.Overlap.
func TestTouchSetsOverlap_LiteralPathHit(t *testing.T) {
	if !touchSetsOverlap([]string{"lib/env-schema.nix"}, []string{"README.md", "lib/env-schema.nix"}) {
		t.Error("expected overlap on lib/env-schema.nix")
	}
}

// declaredOnly converts raw forge.Issue entries into inProgressTouches using
// only their declared touch-set (via the tracker's TouchesOf, not by
// re-parsing body grammar), mirroring v1 behavior for tests that exercise
// overlapsInProgress directly without a PR-file fetch.
func declaredOnly(it forge.IssueTracker, issues []forge.Issue) []inProgressTouches {
	entries := make([]inProgressTouches, len(issues))
	for i, fi := range issues {
		touches, _ := it.TouchesOf(fi.Number)
		entries[i] = inProgressTouches{number: fi.Number, touches: touches}
	}
	return entries
}

// TestOverlapsInProgress_CollidingTouches verifies a candidate's declared
// touch-set overlapping an InProgress issue's declared touch-set is reported,
// naming the colliding issue.
func TestOverlapsInProgress_CollidingTouches(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- lib/env-schema.nix", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- lib/env-schema.nix", State: "OPEN", Labels: []string{"agent-in-progress"}})

	inProgress, err := fc.ListIssues(forge.InProgress)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	collider, held := overlapsInProgress(fc, "10", declaredOnly(fc, inProgress))
	if !held || collider != "20" {
		t.Errorf("overlapsInProgress = (%q, %v), want (\"20\", true)", collider, held)
	}
}

// TestOverlapsInProgress_DisjointTouches verifies disjoint touch-sets never
// hold a candidate.
func TestOverlapsInProgress_DisjointTouches(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- lib/env-schema.nix", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- docs/reference.md", State: "OPEN", Labels: []string{"agent-in-progress"}})

	inProgress, _ := fc.ListIssues(forge.InProgress)
	if _, held := overlapsInProgress(fc, "10", declaredOnly(fc, inProgress)); held {
		t.Error("expected no hold: disjoint touch-sets")
	}
}

// TestOverlapsInProgress_NoDeclaredTouches verifies a candidate with no
// ## Touches section is never held, matching the "dispatched exactly as
// today" acceptance criterion.
func TestOverlapsInProgress_NoDeclaredTouches(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Body: "no touches section here", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- lib/env-schema.nix", State: "OPEN", Labels: []string{"agent-in-progress"}})

	inProgress, _ := fc.ListIssues(forge.InProgress)
	if _, held := overlapsInProgress(fc, "10", declaredOnly(fc, inProgress)); held {
		t.Error("expected no hold: candidate declared no touches")
	}
}

// TestOverlapsInProgress_CollidesViaOpenPRChangedFiles verifies that a
// candidate colliding with an in-progress issue's *actual* PR-changed files —
// not declared in that issue's ## Touches — is still held, per the v2
// acceptance criteria.
func TestOverlapsInProgress_CollidesViaOpenPRChangedFiles(t *testing.T) {
	c := baseConfig()
	c.OverlapGate = "defer"
	branchPrefix := "agent/issue-"
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = branchPrefix
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- internal/pkgx/foo.go", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- docs/reference.md", State: "OPEN", Labels: []string{"agent-in-progress"}})
	fc.SetPR("agent/issue-20", forge.PR{URL: "https://github.com/owner/repo/pull/20"})
	fc.SetPRFiles("https://github.com/owner/repo/pull/20", []string{"internal/pkgx/foo.go"})

	checkOverlap := waveOverlapCheck(c, fc, fc)
	collider, held := checkOverlap("10")
	if !held || collider != "20" {
		t.Errorf("checkOverlap(10) = (%q, %v), want (\"20\", true) via #20's open PR changed files", collider, held)
	}
}

// TestPRTouchesOf_ReturnsOpenPRChangedFiles verifies that on CODE_FORGE=github
// an in-progress issue's open PR changed files are surfaced, so a candidate
// can collide against files the issue itself never declared in ## Touches.
func TestPRTouchesOf_ReturnsOpenPRChangedFiles(t *testing.T) {
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetPR("agent/issue-20", forge.PR{URL: "https://github.com/owner/repo/pull/20"})
	fc.SetPRFiles("https://github.com/owner/repo/pull/20", []string{"lib/env-schema.nix"})

	got := prTouchesOf(fc, "20")
	if len(got) != 1 || got[0] != "lib/env-schema.nix" {
		t.Errorf("prTouchesOf = %v, want [lib/env-schema.nix]", got)
	}
}

// TestPRTouchesOf_NonGithubForgeReturnsNil verifies CODE_FORGE=git — which has
// no PR concept — never attempts a PR-file lookup, matching v1 fallback.
func TestPRTouchesOf_NonGithubForgeReturnsNil(t *testing.T) {
	fc := forge.NewFake()
	fc.SetPR("agent/issue-20", forge.PR{URL: "https://github.com/owner/repo/pull/20"})
	fc.SetPRFiles("https://github.com/owner/repo/pull/20", []string{"lib/env-schema.nix"})

	if got := prTouchesOf(fc.AsPushOnly(), "20"); got != nil {
		t.Errorf("prTouchesOf on a non-github forge = %v, want nil", got)
	}
}

// TestPRTouchesOf_NoOpenPRReturnsNil verifies an in-progress issue with no
// open PR yet contributes nothing extra — no error, no over-blocking.
func TestPRTouchesOf_NoOpenPRReturnsNil(t *testing.T) {
	fc := forge.NewFake()

	if got := prTouchesOf(fc, "20"); got != nil {
		t.Errorf("prTouchesOf with no open PR = %v, want nil", got)
	}
}

// TestPRTouchesOf_ListPRFilesErrorReturnsNil verifies a failed changed-files
// fetch is swallowed rather than propagated — the gate falls back to the
// issue's declared touches instead of erroring the whole check.
func TestPRTouchesOf_ListPRFilesErrorReturnsNil(t *testing.T) {
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetPR("agent/issue-20", forge.PR{URL: "https://github.com/owner/repo/pull/20"})
	fc.PRFilesErr = fmt.Errorf("boom")

	if got := prTouchesOf(fc, "20"); got != nil {
		t.Errorf("prTouchesOf on ListPRFiles error = %v, want nil", got)
	}
}

// TestWaveOverlapCheck_TouchesOfErrorFallsBackToPRFilesOnly verifies that a
// failed it.TouchesOf fetch for one in-progress issue is surfaced via a
// diagnostic (not silently discarded) and does not error the gate — the
// entry still collides via its open PR's changed files, exactly as if its
// declared touches were simply empty.
func TestWaveOverlapCheck_TouchesOfErrorFallsBackToPRFilesOnly(t *testing.T) {
	c := baseConfig()
	c.OverlapGate = "defer"
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- internal/pkgx/foo.go", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- docs/reference.md", State: "OPEN", Labels: []string{"agent-in-progress"}})
	fc.TouchesOfErr = map[string]error{"20": fmt.Errorf("boom")}
	fc.SetPR("agent/issue-20", forge.PR{URL: "https://github.com/owner/repo/pull/20"})
	fc.SetPRFiles("https://github.com/owner/repo/pull/20", []string{"internal/pkgx/foo.go"})

	out := testutil.CaptureStdout(t, func() {
		checkOverlap := waveOverlapCheck(c, fc, fc)
		collider, held := checkOverlap("10")
		if !held || collider != "20" {
			t.Errorf("checkOverlap(10) = (%q, %v), want (\"20\", true) via #20's open PR changed files", collider, held)
		}
	})
	if !strings.Contains(out, "20") {
		t.Errorf("expected a diagnostic naming issue #20's failed TouchesOf fetch, got %q", out)
	}
}
