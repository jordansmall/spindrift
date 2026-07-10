package driver

import "testing"

// TestNewDefaultsToClaude verifies that an empty DRIVER value resolves to the
// "claude" strategy, matching the nix side's default (ADR 0009).
func TestNewDefaultsToClaude(t *testing.T) {
	d, err := New("")
	if err != nil {
		t.Fatalf("New(\"\"): %v", err)
	}
	if d.Name() != "claude" {
		t.Errorf("Name(): got %q, want %q", d.Name(), "claude")
	}
}

// TestNewUnknownDriverErrors verifies that an unrecognized DRIVER value is
// rejected rather than silently falling back.
func TestNewUnknownDriverErrors(t *testing.T) {
	if _, err := New("bogus"); err == nil {
		t.Fatal("New(\"bogus\"): expected error, got nil")
	}
}
