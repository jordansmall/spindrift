package runner

import (
	"errors"
	"testing"
)

// TestFakeRunFuncOverridesDefault verifies that when RunFunc is set, Fake.Run
// calls it instead of consulting RunErrs/RunErr — the seam waves tests use to
// control completion order and timing (e.g. staggered finishes) without real
// sleeps.
func TestFakeRunFuncOverridesDefault(t *testing.T) {
	f := NewFake()
	f.RunErr = errors.New("exit 1")
	var got Box
	f.RunFunc = func(box Box) error {
		got = box
		return nil
	}

	if err := f.Run(Box{Issue: "7"}); err != nil {
		t.Fatalf("Run: got %v, want nil (RunFunc must override RunErr)", err)
	}
	if got.Issue != "7" {
		t.Errorf("RunFunc box: got Issue=%q, want \"7\"", got.Issue)
	}
	if len(f.RunCalls) != 1 || f.RunCalls[0].Issue != "7" {
		t.Errorf("RunCalls: got %v, want one call for issue 7", f.RunCalls)
	}
}
