package forgetest

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// BranchHarness lets RunBranchContract drive the branch capabilities of any
// forge: a push-only forge and a PR forge both answer BranchExists and
// DeleteBranch.
type BranchHarness interface {
	// BranchForge returns the forge under test, backed by a reachable remote.
	BranchForge() forge.CodeForge
	// SeedLandable creates a real branch for issue num and returns its name.
	SeedLandable(num string) string
}

// RunBranchContract checks BranchExists and DeleteBranch against a real
// branch: the seeded one exists, a missing one does not, and a deleted one is
// gone. forge.BranchDeleter's "a missing branch is an error, not a no-op"
// makes deleting a missing branch fail.
func RunBranchContract(t *testing.T, h BranchHarness) {
	t.Run("BranchExists", func(t *testing.T) {
		f, branch := h.BranchForge(), h.SeedLandable("7")
		if ok, err := f.BranchExists(branch); err != nil || !ok {
			t.Fatalf("BranchExists(%q) = (%v, %v), want (true, nil)", branch, ok, err)
		}
		if ok, err := f.BranchExists(branch + "-missing"); err != nil || ok {
			t.Fatalf("BranchExists(missing) = (%v, %v), want (false, nil)", ok, err)
		}
	})
	t.Run("DeleteBranch", func(t *testing.T) {
		f, branch := h.BranchForge(), h.SeedLandable("8")
		d, ok := forge.AsCodeForge[forge.BranchDeleter](f)
		if !ok {
			t.Fatalf("%T has no forge.BranchDeleter layer", f)
		}
		if err := d.DeleteBranch(branch, "main"); err != nil {
			t.Fatalf("DeleteBranch(%q): %v", branch, err)
		}
		if ok, err := f.BranchExists(branch); err != nil || ok {
			t.Fatalf("BranchExists after delete = (%v, %v), want (false, nil)", ok, err)
		}
		if err := d.DeleteBranch(branch, "main"); err == nil {
			t.Fatalf("DeleteBranch(%q) of a missing branch succeeded, want an error", branch)
		}
	})
}
