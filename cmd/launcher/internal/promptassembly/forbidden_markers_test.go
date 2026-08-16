package promptassembly

import (
	"os"
	"testing"
)

// TestLoadForbiddenMarkersParsesAllRows round-trips
// testdata/forbidden-markers.json -- the hand transcription of
// lib/prompt-contract.nix's forbiddenMarkers registry -- into
// []ForbiddenMarkerRow and asserts the decoded fields match.
func TestLoadForbiddenMarkersParsesAllRows(t *testing.T) {
	f, err := os.Open("testdata/forbidden-markers.json")
	if err != nil {
		t.Fatalf("open testdata/forbidden-markers.json: %v", err)
	}
	defer f.Close()

	rows, err := LoadForbiddenMarkers(f)
	if err != nil {
		t.Fatalf("LoadForbiddenMarkers: %v", err)
	}

	want := testForbiddenMarkerRows()
	if len(rows) != len(want) {
		t.Fatalf("len(rows) = %d, want %d", len(rows), len(want))
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("rows[%d] = %+v, want %+v", i, rows[i], want[i])
		}
	}

	// Spot-check a couple of known rows by id/marker.
	if rows[0].ID != "forbidden-git-push" || rows[0].Marker != "git push" {
		t.Errorf("rows[0] = %+v, want id=forbidden-git-push marker=%q", rows[0], "git push")
	}
	if rows[6].ID != "forbidden-git-bundle-create" || rows[6].Marker != "git bundle create" {
		t.Errorf("rows[6] = %+v, want id=forbidden-git-bundle-create marker=%q", rows[6], "git bundle create")
	}
	if rows[7].ID != "forbidden-gh-api-mutation" || rows[7].Marker != "gh api" {
		t.Errorf("rows[7] = %+v, want id=forbidden-gh-api-mutation marker=%q", rows[7], "gh api")
	}
	if rows[8].ID != "forbidden-fj-pr-create" || rows[8].Marker != "fj pr create" {
		t.Errorf("rows[8] = %+v, want id=forbidden-fj-pr-create marker=%q", rows[8], "fj pr create")
	}
	if rows[12].ID != "forbidden-fj-issue-create" || rows[12].Marker != "fj issue create" {
		t.Errorf("rows[12] = %+v, want id=forbidden-fj-issue-create marker=%q", rows[12], "fj issue create")
	}
}

// TestLoadForbiddenMarkersMalformed covers the error path: invalid JSON must
// return a non-nil, wrapped error, never panic.
func TestLoadForbiddenMarkersMalformed(t *testing.T) {
	f, err := os.Open("testdata/malformed.json")
	if err != nil {
		t.Fatalf("open testdata/malformed.json: %v", err)
	}
	defer f.Close()

	if _, err := LoadForbiddenMarkers(f); err == nil {
		t.Fatal("LoadForbiddenMarkers(malformed) = nil error, want non-nil")
	}
}

// TestLoadForbiddenMarkersFileMalformed exercises LoadForbiddenMarkersFile's
// own error path alongside LoadForbiddenMarkers's.
func TestLoadForbiddenMarkersFileMalformed(t *testing.T) {
	if _, err := LoadForbiddenMarkersFile("testdata/malformed.json"); err == nil {
		t.Fatal("LoadForbiddenMarkersFile(malformed) = nil error, want non-nil")
	}
}

// TestLoadForbiddenMarkersFileNonexistent covers a nonexistent path: a
// wrapped, non-nil error, never a panic.
func TestLoadForbiddenMarkersFileNonexistent(t *testing.T) {
	if _, err := LoadForbiddenMarkersFile("testdata/does-not-exist.json"); err == nil {
		t.Fatal("LoadForbiddenMarkersFile(nonexistent) = nil error, want non-nil")
	}
}

// testForbiddenMarkerRows returns the thirteen forbiddenMarkers rows in
// lib/prompt-contract.nix's own order, for tests that don't need to load
// them from testdata/forbidden-markers.json (a later slice's Validate tests
// use this directly).
func testForbiddenMarkerRows() []ForbiddenMarkerRow {
	return []ForbiddenMarkerRow{
		{
			ID:       "forbidden-git-push",
			Marker:   "git push",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "git-hook",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'git push' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-gh-pr-create",
			Marker:   "gh pr create",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "command-shim",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-gh-pr-ready",
			Marker:   "gh pr ready",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "command-shim",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr ready' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-gh-pr-merge",
			Marker:   "gh pr merge",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "command-shim",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr merge' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-gh-issue-comment",
			Marker:   "gh issue comment",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "command-shim",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh issue comment' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-gh-issue-create",
			Marker:   "gh issue create",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "command-shim",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh issue create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-git-bundle-create",
			Marker:   "git bundle create",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "prompt-only",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'git bundle create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-gh-api-mutation",
			Marker:   "gh api",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "gh-api-mutation",
			Enforce:  "command-shim",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh api' with a mutating method (-X/--method POST/PATCH/PUT/DELETE) -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; make this change through the same relay a `gh pr create`/`gh issue create`/`gh issue comment` write would use. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-fj-pr-create",
			Marker:   "fj pr create",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "command-shim",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'fj pr create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; forgejo PRs are opened via the PR-intent relay (SPINDRIFT_PR_INTENT), the same host-mediated relay a read-only github Box uses for `gh pr create`, applied over the forgejo relay path. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-fj-pr-ready",
			Marker:   "fj pr ready",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "command-shim",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'fj pr ready' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; the launcher flips the PR ready once CI is green over the forgejo relay path, so a Box must never run 'fj pr ready' itself. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-fj-pr-merge",
			Marker:   "fj pr merge",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "command-shim",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'fj pr merge' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; the launcher merges the PR once CI is green over the forgejo relay path, so a Box must never run 'fj pr merge' itself. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-fj-issue-comment",
			Marker:   "fj issue comment",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "command-shim",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'fj issue comment' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; issue comments are relayed via the outcome contract's `note=` field, the same relay a read-only github Box uses for `gh issue comment`, applied over the forgejo path. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-fj-issue-create",
			Marker:   "fj issue create",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Kind:     "substring",
			Enforce:  "command-shim",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'fj issue create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; issues are filed via the issue-intent relay (SPINDRIFT_ISSUE_INTENT), the same relay a read-only github Box uses for `gh issue create`, applied over the forgejo path. Refusing to invoke the Driver.",
		},
	}
}
