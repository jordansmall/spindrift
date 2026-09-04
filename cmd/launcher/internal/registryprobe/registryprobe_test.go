package registryprobe

import "testing"

// TestReservedExitCodes_DistinctAndUnclaimed pins the properties the whole
// scheme depends on: an old driver-exec (no probe verb) can only ever exit 0,
// 1, or 2, so neither reserved code may equal any of those, and neither may
// collide with docker/podman's own reserved 125/126/127.
func TestReservedExitCodes_DistinctAndUnclaimed(t *testing.T) {
	if ExitCapable == ExitIncapable {
		t.Fatalf("ExitCapable and ExitIncapable must differ, both = %d", ExitCapable)
	}
	for _, unclaimable := range []int{0, 1, 2, 125, 126, 127} {
		if ExitCapable == unclaimable {
			t.Errorf("ExitCapable = %d, collides with reserved/producible code %d", ExitCapable, unclaimable)
		}
		if ExitIncapable == unclaimable {
			t.Errorf("ExitIncapable = %d, collides with reserved/producible code %d", ExitIncapable, unclaimable)
		}
	}
}
