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

// testForbiddenMarkerRows returns the seven forbiddenMarkers rows in
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
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'git push' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-gh-pr-create",
			Marker:   "gh pr create",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-gh-pr-ready",
			Marker:   "gh pr ready",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr ready' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-gh-pr-merge",
			Marker:   "gh pr merge",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr merge' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-gh-issue-comment",
			Marker:   "gh issue comment",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh issue comment' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-gh-issue-create",
			Marker:   "gh issue create",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh issue create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
		{
			ID:       "forbidden-git-bundle-create",
			Marker:   "git bundle create",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "boxAccessReadOnly",
			Message:  "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'git bundle create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.",
		},
	}
}
