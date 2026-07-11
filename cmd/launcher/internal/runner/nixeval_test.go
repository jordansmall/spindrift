package runner

import "testing"

// TestNixEvalRef_BuildsHermeticGitFileRef verifies that the flake reference
// passed to `nix eval` points at the fetched rev via a hermetic git+file URL
// — never the working tree — with .drvPath appended to the attr.
func TestNixEvalRef_BuildsHermeticGitFileRef(t *testing.T) {
	got := nixEvalRef("/repo", "deadbeef", "packages.x86_64-linux.agent-image")
	want := "git+file:///repo?rev=deadbeef#packages.x86_64-linux.agent-image.drvPath"
	if got != want {
		t.Errorf("nixEvalRef = %q, want %q", got, want)
	}
}
