package waves

import (
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

// touchSetsOverlap delegates per-pair glob semantics to glob.Overlap, which
// internal/glob pins, so this only checks that one matching pair is enough.
func TestTouchSetsOverlap_LiteralPathHit(t *testing.T) {
	if !touchSetsOverlap([]string{"lib/env-schema.nix"}, []string{"README.md", "lib/env-schema.nix"}) {
		t.Error("expected overlap on lib/env-schema.nix")
	}
}

// declaredOnly builds entries from the tracker's TouchesOf alone, mirroring v1
// behavior, so a test can exercise overlapsInProgress without a PR-file fetch.
func declaredOnly(it forge.IssueTracker, issues []forge.Issue) []inProgressTouches {
	entries := make([]inProgressTouches, len(issues))
	for i, fi := range issues {
		touches, _ := it.TouchesOf(fi.Number)
		entries[i] = inProgressTouches{number: fi.Number, touches: touches}
	}
	return entries
}

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

func TestOverlapsInProgress_DisjointTouches(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- lib/env-schema.nix", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- docs/reference.md", State: "OPEN", Labels: []string{"agent-in-progress"}})

	inProgress, _ := fc.ListIssues(forge.InProgress)
	if _, held := overlapsInProgress(fc, "10", declaredOnly(fc, inProgress)); held {
		t.Error("expected no hold: disjoint touch-sets")
	}
}

// A candidate with no ## Touches section must dispatch exactly as it did
// before the gate existed, so it is never held.
func TestOverlapsInProgress_NoDeclaredTouches(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Body: "no touches section here", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- lib/env-schema.nix", State: "OPEN", Labels: []string{"agent-in-progress"}})

	inProgress, _ := fc.ListIssues(forge.InProgress)
	if _, held := overlapsInProgress(fc, "10", declaredOnly(fc, inProgress)); held {
		t.Error("expected no hold: candidate declared no touches")
	}
}

// A failed TouchesOf fetch for the candidate itself, unlike one for an
// in-progress entry, fails open as "no declared touches" and stays silent, so
// this also asserts that nothing is printed.
func TestOverlapsInProgress_CandidateTouchesOfErrorReturnsNoCollision(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- lib/env-schema.nix", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- lib/env-schema.nix", State: "OPEN", Labels: []string{"agent-in-progress"}})
	fc.TouchesOfErr = map[string]error{"10": fmt.Errorf("boom")}

	inProgress, _ := fc.ListIssues(forge.InProgress)
	out := testutil.CaptureStdout(t, func() {
		if _, held := overlapsInProgress(fc, "10", declaredOnly(fc, inProgress)); held {
			t.Error("expected no hold: candidate TouchesOf fetch failed")
		}
	})
	if out != "" {
		t.Errorf("expected no diagnostic for a candidate-side TouchesOf error, got %q", out)
	}
}

// The v2 gate holds a candidate that collides with an in-progress issue's real
// PR-changed files, which is why #20 declares docs/reference.md and changes a
// different file.
func TestOverlapsInProgress_CollidesViaOpenPRChangedFiles(t *testing.T) {
	c := baseConfig()
	c.OverlapGate = "defer"
	branchPrefix := "agent/issue-"
	fc := forge.NewFake(dispatchLabels(c, ""))
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

// On CODE_FORGE=github the open PR's changed files are returned so a candidate
// can collide against files the issue never declared in ## Touches.
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

// CODE_FORGE=git has no PR concept, so prTouchesOf must not attempt a PR-file
// lookup even when the fake holds one.
func TestPRTouchesOf_NonGithubForgeReturnsNil(t *testing.T) {
	fc := forge.NewFake()
	fc.SetPR("agent/issue-20", forge.PR{URL: "https://github.com/owner/repo/pull/20"})
	fc.SetPRFiles("https://github.com/owner/repo/pull/20", []string{"lib/env-schema.nix"})

	if got := prTouchesOf(fc.AsPushOnly(), "20"); got != nil {
		t.Errorf("prTouchesOf on a non-github forge = %v, want nil", got)
	}
}

func TestPRTouchesOf_NoOpenPRReturnsNil(t *testing.T) {
	fc := forge.NewFake()

	if got := prTouchesOf(fc, "20"); got != nil {
		t.Errorf("prTouchesOf with no open PR = %v, want nil", got)
	}
}

// A failed changed-files fetch is swallowed rather than propagated, so the
// gate falls back to the issue's declared touches instead of erroring.
func TestPRTouchesOf_ListPRFilesErrorReturnsNil(t *testing.T) {
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetPR("agent/issue-20", forge.PR{URL: "https://github.com/owner/repo/pull/20"})
	fc.PRFilesErr = fmt.Errorf("boom")

	if got := prTouchesOf(fc, "20"); got != nil {
		t.Errorf("prTouchesOf on ListPRFiles error = %v, want nil", got)
	}
}

// A failed TouchesOf fetch for an in-progress issue prints a diagnostic rather
// than being discarded silently, and the entry still collides through its open
// PR's changed files as if its declared touches were empty.
func TestWaveOverlapCheck_TouchesOfErrorFallsBackToPRFilesOnly(t *testing.T) {
	c := baseConfig()
	c.OverlapGate = "defer"
	fc := forge.NewFake(dispatchLabels(c, ""))
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

// When TouchesOf fails for an in-progress issue that has no open PR, there is
// nothing to fall back to, so the diagnostic must not claim a PR-files
// fallback.
func TestWaveOverlapCheck_TouchesOfErrorNoOpenPRDoesNotClaimFallback(t *testing.T) {
	c := baseConfig()
	c.OverlapGate = "defer"
	fc := forge.NewFake(dispatchLabels(c, ""))
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- internal/pkgx/foo.go", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- docs/reference.md", State: "OPEN", Labels: []string{"agent-in-progress"}})
	fc.TouchesOfErr = map[string]error{"20": fmt.Errorf("boom")}
	// Issue #20 deliberately gets no fc.SetPR call, so it has no open PR.

	out := testutil.CaptureStdout(t, func() {
		waveOverlapCheck(c, fc, fc)
	})
	if strings.Contains(out, "falling back to its open PR's changed files") {
		t.Errorf("diagnostic falsely claims a PR-files fallback with no open PR: %q", out)
	}
	if !strings.Contains(out, "no PR-changed-files available to fall back to") {
		t.Errorf("expected a diagnostic naming the no-fallback case, got %q", out)
	}
}
