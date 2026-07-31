package forgejo_test

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge/forgejo"
)

// TestValidateForgejoEnv_RequiresBaseURLAndToken verifies ValidateForgejoEnv
// fails fast when either required Forgejo connection field is empty, rather
// than deferring to a runtime Forgejo API error.
func TestValidateForgejoEnv_RequiresBaseURLAndToken(t *testing.T) {
	if err := forgejo.ValidateForgejoEnv("https://codeberg.org", "tok"); err != nil {
		t.Fatalf("fully configured forgejo env should validate: %v", err)
	}
	if err := forgejo.ValidateForgejoEnv("", "tok"); err == nil || !strings.Contains(err.Error(), "FORGEJO_BASE_URL") {
		t.Errorf("ValidateForgejoEnv should require FORGEJO_BASE_URL, got: %v", err)
	}
	if err := forgejo.ValidateForgejoEnv("https://codeberg.org", ""); err == nil || !strings.Contains(err.Error(), "FORGEJO_TOKEN") {
		t.Errorf("ValidateForgejoEnv should require FORGEJO_TOKEN, got: %v", err)
	}
}
