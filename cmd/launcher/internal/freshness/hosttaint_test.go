package freshness

import (
	"testing"
)

// A stale verdict at the same rev the prior launcher process exited stale on
// means a rebuild happened without the rev moving, so the divergence is
// structural rather than stale content.
func TestNonConverging_SameStaleRevAsPrior(t *testing.T) {
	if got := NonConverging("deadbeef", "deadbeef"); !got {
		t.Errorf("NonConverging(%q, %q) = false, want true", "deadbeef", "deadbeef")
	}
}

// The base tip moved since the last stale exit, so this can be ordinary
// content staleness that a rebuild resolves.
func TestNonConverging_DifferentPriorRev(t *testing.T) {
	if got := NonConverging("deadbeef", "priorrev"); got {
		t.Errorf("NonConverging(%q, %q) = true, want false", "deadbeef", "priorrev")
	}
}

// An empty staleRev means no rev was fetched, for instance because the fetch
// failed, so there is nothing to compare against priorStaleRev.
func TestNonConverging_EmptyStaleRev(t *testing.T) {
	if got := NonConverging("", "deadbeef"); got {
		t.Errorf("NonConverging(%q, %q) = true, want false", "", "deadbeef")
	}
	if got := NonConverging("", ""); got {
		t.Errorf("NonConverging(%q, %q) = true, want false", "", "")
	}
}
