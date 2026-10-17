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

// TestProviderNamespacedModels pins the Go mirror of the Nix
// providerNamespacedModels fact, including names New would reject.
func TestProviderNamespacedModels(t *testing.T) {
	for name, want := range map[string]bool{
		"opencode": true,
		"claude":   false,
		"":         false,
		"nope":     false,
	} {
		if got := ProviderNamespacedModels(name); got != want {
			t.Errorf("ProviderNamespacedModels(%q) = %v, want %v", name, got, want)
		}
	}
}
