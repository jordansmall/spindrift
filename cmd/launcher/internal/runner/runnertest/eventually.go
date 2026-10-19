package runnertest

import (
	"testing"
	"time"
)

// Eventually polls cond every few milliseconds until it holds, failing the
// test with what if timeout elapses first.
func Eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s not true within %s", what, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
