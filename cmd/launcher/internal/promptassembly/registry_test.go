package promptassembly

import (
	"os"
	"strings"
	"testing"
)

// TestLoadRegistryParsesAllRows loads testdata/registry.json — the hand
// transcription of every row in lib/fragments.nix (67 rows as of issue
// #2526, which removed the LAND_GIT_PUSH_READ_ONLY_STEP row added by issue
// #2510: an eval-time assert now makes BOX_FORGE_AND_ISSUE_ACCESS=read-only
// paired with CODE_FORGE=git unbuildable, so no image needing that step can
// exist) — and spot-checks a handful of known rows rather than asserting
// the full payload verbatim, so this test doesn't itself become the thing
// that silently drifts from fragments.nix.
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

	const wantRows = 67
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
