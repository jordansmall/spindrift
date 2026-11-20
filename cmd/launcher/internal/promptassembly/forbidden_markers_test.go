package promptassembly

import (
	"os"
	"testing"
)

// testdata/forbidden-markers.json is a hand transcription of
// lib/prompt-contract.nix's forbiddenMarkers registry, so this test catches a
// transcription that has drifted from the registry as well as a decode bug.
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

	if rows[0].ID != "forbidden-git-push" || rows[0].Marker != "git push" {
		t.Errorf("rows[0] = %+v, want id=forbidden-git-push marker=%q", rows[0], "git push")
	}
	if rows[1].ID != "forbidden-git-bundle-create" || rows[1].Marker != "git bundle create" {
		t.Errorf("rows[1] = %+v, want id=forbidden-git-bundle-create marker=%q", rows[1], "git bundle create")
	}
}

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

func TestLoadForbiddenMarkersFileMalformed(t *testing.T) {
	if _, err := LoadForbiddenMarkersFile("testdata/malformed.json"); err == nil {
		t.Fatal("LoadForbiddenMarkersFile(malformed) = nil error, want non-nil")
	}
}

func TestLoadForbiddenMarkersFileNonexistent(t *testing.T) {
	if _, err := LoadForbiddenMarkersFile("testdata/does-not-exist.json"); err == nil {
		t.Fatal("LoadForbiddenMarkersFile(nonexistent) = nil error, want non-nil")
	}
}

// The rows keep lib/prompt-contract.nix's own order: the parse test compares
// them to the decoded rows element by element, so the order is part of the
// assertion.
func testForbiddenMarkerRows() []ForbiddenMarkerRow {
	return []ForbiddenMarkerRow{
		{
			ID:             "forbidden-git-push",
			Marker:         "git push",
			Carrier:        "fragment-body",
			Severity:       "reject",
			When:           "boxAccessReadOnly",
			Kind:           "substring",
			Enforce:        "git-hook",
			Message:        "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'git push' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; the committed branch is relayed via the outbox instead, so a Box must never run 'git push' itself. Refusing to invoke the Driver.",
			RuntimeMessage: "read-only Box: your committed branch is relayed via the outbox; do not push -- this push has been blocked locally.",
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
	}
}
