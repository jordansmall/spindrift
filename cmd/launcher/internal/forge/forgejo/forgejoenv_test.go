package forgejo_test

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge/forgejo"
)

// TestValidateForgejoEnv_RequiresBaseURLAndToken verifies ValidateForgejoEnv
// fails fast when either required Forgejo connection field is empty, rather
// than deferring to a runtime Forgejo API error, and names the knob that
// required them.
func TestValidateForgejoEnv_RequiresBaseURLAndToken(t *testing.T) {
	for _, tc := range []struct{ knob, other string }{
		{"ISSUE_TRACKER", "CODE_FORGE"},
		{"CODE_FORGE", "ISSUE_TRACKER"},
	} {
		knob, other := tc.knob, tc.other
		t.Run(knob, func(t *testing.T) {
			if err := forgejo.ValidateForgejoEnv(knob, "https://codeberg.org", "tok"); err != nil {
				t.Fatalf("fully configured forgejo env should validate: %v", err)
			}
			want := knob + "=forgejo"
			if err := forgejo.ValidateForgejoEnv(knob, "", "tok"); err == nil ||
				!strings.Contains(err.Error(), "FORGEJO_BASE_URL") || !strings.Contains(err.Error(), want) ||
				strings.Contains(err.Error(), other) {
				t.Errorf("ValidateForgejoEnv should require FORGEJO_BASE_URL when %s, not %s, got: %v", want, other, err)
			}
			if err := forgejo.ValidateForgejoEnv(knob, "https://codeberg.org", ""); err == nil ||
				!strings.Contains(err.Error(), "FORGEJO_TOKEN") || !strings.Contains(err.Error(), want) ||
				strings.Contains(err.Error(), other) {
				t.Errorf("ValidateForgejoEnv should require FORGEJO_TOKEN when %s, not %s, got: %v", want, other, err)
			}
		})
	}
}
