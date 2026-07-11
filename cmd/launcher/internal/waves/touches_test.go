package waves

import (
	"fmt"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestTouchSetsOverlap_LiteralPathHit verifies that touchSetsOverlap checks
// every pattern pair across the two sets, not just the first (glob-semantics
// per-pair matching is pinned in internal/glob).
func TestTouchSetsOverlap_LiteralPathHit(t *testing.T) {
	if !touchSetsOverlap([]string{"lib/env-schema.nix"}, []string{"lib/env-schema.nix", "README.md"}) {
		t.Error("expected overlap on lib/env-schema.nix")
	}
}

// declaredOnly converts raw forge.Issue entries into inProgressTouches using
// only their declared ## Touches section, mirroring v1 behavior for tests
// that exercise overlapsInProgress directly without a PR-file fetch.
func declaredOnly(issues []forge.Issue) []inProgressTouches {
	entries := make([]inProgressTouches, len(issues))
	for i, fi := range issues {
		entries[i] = inProgressTouches{number: fi.Number, touches: forge.ParseTouchPaths(fi.Body)}
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
	collider, held := overlapsInProgress(fc, "10", declaredOnly(inProgress))
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
	if _, held := overlapsInProgress(fc, "10", declaredOnly(inProgress)); held {
		t.Error("expected no hold: disjoint touch-sets")
	}
}

// TestBatchHasTouchOverlap_DetectsOverlap verifies a batch containing an
// issue whose declared touches overlap an in-progress issue's is reported.
func TestBatchHasTouchOverlap_DetectsOverlap(t *testing.T) {
	c := Config{OverlapGate: "defer", InProgressLabel: "agent-in-progress"}
	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- lib/env-schema.nix", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- lib/env-schema.nix", State: "OPEN", Labels: []string{c.InProgressLabel}})

	if !batchHasTouchOverlap(c, fc, []Issue{{Number: "10"}}) {
		t.Error("expected batch overlap to be detected")
	}
}

// TestBatchHasTouchOverlap_GateOffNeverOverlaps verifies OVERLAP_GATE=off
// short-circuits the check regardless of declared touches.
func TestBatchHasTouchOverlap_GateOffNeverOverlaps(t *testing.T) {
	c := Config{OverlapGate: "off", InProgressLabel: "agent-in-progress"}
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- lib/env-schema.nix", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- lib/env-schema.nix", State: "OPEN", Labels: []string{c.InProgressLabel}})

	if batchHasTouchOverlap(c, fc, []Issue{{Number: "10"}}) {
		t.Error("expected OVERLAP_GATE=off to disable the check")
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
	if _, held := overlapsInProgress(fc, "10", declaredOnly(inProgress)); held {
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

	checkOverlap := waveOverlapCheck(c, fc)
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
	fc.IsPushOnly = true
	fc.SetPR("agent/issue-20", forge.PR{URL: "https://github.com/owner/repo/pull/20"})
	fc.SetPRFiles("https://github.com/owner/repo/pull/20", []string{"lib/env-schema.nix"})

	if got := prTouchesOf(fc, "20"); got != nil {
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
