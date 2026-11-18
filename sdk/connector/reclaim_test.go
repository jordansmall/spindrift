package connector

import "testing"

func TestReclaimDue(t *testing.T) {
	reset := func() { sinceReclaim = struct{ bytes, calls int }{} }
	t.Cleanup(reset)

	t.Run("fires at the call count with tiny requests, then resets", func(t *testing.T) {
		reset()
		for i := 1; i < reclaimCalls; i++ {
			if reclaimDue(10) {
				t.Fatalf("fired at call %d, want %d", i, reclaimCalls)
			}
		}
		if !reclaimDue(10) {
			t.Fatalf("did not fire at call %d", reclaimCalls)
		}
		if reclaimDue(10) {
			t.Fatal("fired on the call right after a reclaim")
		}
	})

	t.Run("fires on one large request", func(t *testing.T) {
		reset()
		if !reclaimDue(reclaimBytes) {
			t.Fatal("did not fire on a request of reclaimBytes")
		}
	})

	t.Run("fires when response bytes push it over", func(t *testing.T) {
		reset()
		if reclaimDue(100) {
			t.Fatal("fired on a small request")
		}
		addReclaimBytes(reclaimBytes)
		if !reclaimDue(100) {
			t.Fatal("did not fire after response bytes crossed the threshold")
		}
		if reclaimDue(100) {
			t.Fatal("bytes were not reset after firing")
		}
	})
}
