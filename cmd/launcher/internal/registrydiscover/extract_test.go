package registrydiscover

import (
	"testing"
)

// TestExtract_NoConfigFilesYieldsEmptyResults verifies that a directory with
// none of the recognized config files yields empty Declaration and Note slices,
// not an error.
func TestExtract_NoConfigFilesYieldsEmptyResults(t *testing.T) {
	dir := t.TempDir()

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Errorf("declared = %+v, want none", declared)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %+v, want none", notes)
	}
}
