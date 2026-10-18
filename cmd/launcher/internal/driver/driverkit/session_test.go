package driverkit

import "testing"

func TestSessionUUID(t *testing.T) {
	// Pinned literally: changing the key derivation orphans every claude and pi
	// session already on disk.
	const want = "bf718c96-aa0c-c4fa-d029-3a6bbb980c63"
	got := SessionUUID("owner/repo", "7")
	if got != want {
		t.Errorf("SessionUUID = %q, want %q", got, want)
	}
	if other := SessionUUID("owner/repo", "8"); other == got {
		t.Errorf("distinct issues share id %q", got)
	}
}
