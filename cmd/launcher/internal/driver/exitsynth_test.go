package driver

import "testing"

// TestOpencodeDriverImplementsExitSynthesizer verifies that New("opencode")
// satisfies the optional ExitSynthesizer interface — the opencode CLI's own
// exit code is unreliable (it exits 0 even on a mid-run error), so the
// launcher must be able to derive a trustworthy exit code from the log.
func TestOpencodeDriverImplementsExitSynthesizer(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}
	if _, ok := d.(ExitSynthesizer); !ok {
		t.Error("New(opencode) does not implement ExitSynthesizer")
	}
}

// TestClaudeDriverDoesNotImplementExitSynthesizer verifies that New("claude")
// does NOT satisfy ExitSynthesizer — claude's own exit code is already
// trustworthy, so it has no need for this optional capability.
func TestClaudeDriverDoesNotImplementExitSynthesizer(t *testing.T) {
	d, err := New("claude")
	if err != nil {
		t.Fatalf("New(claude): %v", err)
	}
	if _, ok := d.(ExitSynthesizer); ok {
		t.Error("New(claude) unexpectedly implements ExitSynthesizer")
	}
}
