package promptassembly

import (
	"os"
	"strings"
	"testing"
)

// TestLoadRegistryParsesAllRows loads testdata/registry.json — the hand
// transcription of every row in lib/fragments.nix (73 rows, reconciled via
// `git log --oneline -- lib/fragments.nix` as: 67 as of issue #2526's
// removal (commit 44d101bd) of the LAND_GIT_PUSH_READ_ONLY_STEP row issue
// #2510 had added -- an eval-time assert now makes
// BOX_FORGE_AND_ISSUE_ACCESS=read-only paired with CODE_FORGE=git
// unbuildable, so no image needing that step can exist -- plus 2 for the
// LAND_GIT_STOP_READ_WRITE_STEP/LAND_GIT_STOP_READ_ONLY_STEP pair this same
// issue's review pass (commit 6b275e1b) added right after, to fix the
// orphaned "2." the LAND_GIT_PUSH_READ_ONLY_STEP removal left behind in the
// CODE_FORGE=git block's own final step (69), plus 1 for the
// COMMIT_REWORK_ORCHESTRATOR_STEP row issue #2698 added (commit 48bef325)
// on the existing REVIEW_LOOP_ORCHESTRATOR gate (70), minus 1 for the
// COORDINATOR_PARALLEL_STEP row issue #2061/#2497's parallel-dispatch
// removal (commit 45679577) dropped (69), plus 1 for the CAVEMAN_STEP_WORKER
// row issue #2706 added (commit e3cfc7cd) on the existing CAVEMAN_BAKED gate
// (70), plus 1 for the CAVEMAN_STEP_REVIEW row issue #2707 added (commit
// b27ed6eb) on the same CAVEMAN_BAKED gate (71), plus 1 for the
// CAVEMAN_STEP_RESEARCH row issue #2708 added (commit 48ba64a2, this
// branch) on the same CAVEMAN_BAKED gate (72), plus 1 for the
// RESEARCH_FILE_ISSUES_RELAY_STEP row issue #2593/ADR 0041 added on the
// existing FILER_FILE_RELAY gate (73), plus 1 for the
// FILER_LABEL_RELAY_RESEARCH_STEP row a review finding on issue #2593
// added (this branch): filer-label-relay.md's write-mechanism gate split
// from the combined FILER_FILE_RELAY into FILER_FILE_RELAY_WORK (kept on
// the existing filer-label-relay.md row) and FILER_FILE_RELAY_RESEARCH (this
// new row, on the new filer-label-relay-research.md fragment) (74), plus 1
// for the CODE_COMMENTS_STEP row issue #2880 added on the new always-true
// CODE_COMMENTS_MANDATORY gate (75)) — and
// spot-checks a handful of known rows rather than asserting the full
// payload verbatim, so this test doesn't itself become the thing that
// silently drifts from fragments.nix.
func TestLoadRegistryParsesAllRows(t *testing.T) {
	f, err := os.Open("testdata/registry.json")
	if err != nil {
		t.Fatalf("open testdata/registry.json: %v", err)
	}
	defer f.Close()

	reg, err := LoadRegistry(f)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	const wantRows = 75
	if len(reg.Rows) != wantRows {
		t.Fatalf("len(reg.Rows) = %d, want %d", len(reg.Rows), wantRows)
	}

	first := reg.Rows[0]
	wantFirst := FragmentRow{
		Gate:           "SKILLS_FOUND",
		Fragment:       "skill-preamble.md",
		Var:            "SKILL_PREAMBLE",
		ExtraSubstVars: []string{"SKILLS_FOUND"},
	}
	if first.Gate != wantFirst.Gate || first.Fragment != wantFirst.Fragment || first.Var != wantFirst.Var {
		t.Errorf("reg.Rows[0] = %+v, want %+v", first, wantFirst)
	}
	if len(first.ExtraSubstVars) != 1 || first.ExtraSubstVars[0] != "SKILLS_FOUND" {
		t.Errorf("reg.Rows[0].ExtraSubstVars = %v, want %v", first.ExtraSubstVars, wantFirst.ExtraSubstVars)
	}

	// A plain row with no extraSubstVars in the source JSON must default to
	// an empty/nil slice, never a spurious entry.
	caveman := reg.Rows[1]
	if caveman.Gate != "CAVEMAN_BAKED" || caveman.Fragment != "caveman-default.md" || caveman.Var != "CAVEMAN_STEP" {
		t.Errorf("reg.Rows[1] = %+v, want CAVEMAN_BAKED/caveman-default.md/CAVEMAN_STEP", caveman)
	}
	if len(caveman.ExtraSubstVars) != 0 {
		t.Errorf("reg.Rows[1].ExtraSubstVars = %v, want empty", caveman.ExtraSubstVars)
	}

	// The other extraSubstVars row (ci-failure.md), per fragments.nix's
	// header comment naming exactly these two as the only ones that
	// interpolate a variable inside their own body.
	var ciFailure *FragmentRow
	for i := range reg.Rows {
		if reg.Rows[i].Fragment == "ci-failure.md" {
			ciFailure = &reg.Rows[i]
			break
		}
	}
	if ciFailure == nil {
		t.Fatal("no ci-failure.md row found")
	}
	if ciFailure.Gate != "CI_FAILURE_SUMMARY" || ciFailure.Var != "CI_FAILURE_STEP" {
		t.Errorf("ci-failure.md row = %+v, want gate CI_FAILURE_SUMMARY, var CI_FAILURE_STEP", ciFailure)
	}
	if len(ciFailure.ExtraSubstVars) != 1 || ciFailure.ExtraSubstVars[0] != "CI_FAILURE_SUMMARY" {
		t.Errorf("ci-failure.md row ExtraSubstVars = %v, want [CI_FAILURE_SUMMARY]", ciFailure.ExtraSubstVars)
	}

	// Exactly two rows carry extraSubstVars, matching fragments.nix's header
	// comment.
	withExtra := 0
	for _, r := range reg.Rows {
		if len(r.ExtraSubstVars) > 0 {
			withExtra++
		}
	}
	if withExtra != 2 {
		t.Errorf("rows with ExtraSubstVars = %d, want 2", withExtra)
	}
}

// TestLoadRegistryMalformed covers the error path: invalid JSON must return
// a non-nil, wrapped error, never panic.
func TestLoadRegistryMalformed(t *testing.T) {
	f, err := os.Open("testdata/malformed.json")
	if err != nil {
		t.Fatalf("open testdata/malformed.json: %v", err)
	}
	defer f.Close()

	if _, err := LoadRegistry(f); err == nil {
		t.Fatal("LoadRegistry(malformed) = nil error, want non-nil")
	}
}

// TestLoadRegistryFileMalformed exercises LoadRegistryFile's own error path
// alongside LoadRegistry's.
func TestLoadRegistryFileMalformed(t *testing.T) {
	if _, err := LoadRegistryFile("testdata/malformed.json"); err == nil {
		t.Fatal("LoadRegistryFile(malformed) = nil error, want non-nil")
	}
}

// TestLoadRegistryFileNonexistent covers a nonexistent path: a wrapped,
// non-nil error, never a panic.
func TestLoadRegistryFileNonexistent(t *testing.T) {
	if _, err := LoadRegistryFile("testdata/does-not-exist.json"); err == nil {
		t.Fatal("LoadRegistryFile(nonexistent) = nil error, want non-nil")
	}
}

// TestLoadRegistryEmptyReader covers an empty reader (io.EOF from the JSON
// decoder before any token is read): a non-nil error, never a panic.
func TestLoadRegistryEmptyReader(t *testing.T) {
	if _, err := LoadRegistry(strings.NewReader("")); err == nil {
		t.Fatal("LoadRegistry(empty reader) = nil error, want non-nil")
	}
}
