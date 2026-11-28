package forgejo_test

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge/forgejo"
)

// TestValidateForgejoEnv_RequiresBaseURL verifies ValidateForgejoEnv
// fails fast on an empty field, naming the knob that required it.
func TestValidateForgejoEnv_RequiresBaseURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ knob, other string }{
		{"ISSUE_TRACKER", "CODE_FORGE"},
		{"CODE_FORGE", "ISSUE_TRACKER"},
	} {
		knob, other := tc.knob, tc.other
		t.Run(knob, func(t *testing.T) {
			if err := forgejo.ValidateForgejoEnv(knob, "https://codeberg.org"); err != nil {
				t.Fatalf("fully configured forgejo env should validate: %v", err)
			}
			want := knob + "=forgejo"
			if err := forgejo.ValidateForgejoEnv(knob, ""); err == nil ||
				!strings.Contains(err.Error(), "--forgejo-base-url") || !strings.Contains(err.Error(), want) ||
				strings.Contains(err.Error(), other) {
				t.Errorf("ValidateForgejoEnv should require --forgejo-base-url when %s, not %s, got: %v", want, other, err)
			}
		})
	}
}
