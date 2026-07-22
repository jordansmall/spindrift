package localloop_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/localloop"
)

// TestVerdict_String_Surfaced verifies a surfaced Verdict renders the exact
// line shape issue #1811 specifies: "surfaced → branch <name> (N seams)",
// prefixed with the broad ticket's own key so a mixed-parent sweep's lines
// stay attributable.
func TestVerdict_String_Surfaced(t *testing.T) {
	v := localloop.Verdict{Parent: local.ResolveParent("42", ""), Kind: localloop.VerdictSurfaced, Branch: "seam-42", SeamCount: 3}
	want := "surface: 42 surfaced → branch seam-42 (3 seams)"
	if got := v.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// TestVerdict_String_Held verifies a held Verdict renders the exact line
// shape issue #1811 specifies: "held — <first unmet gate>".
func TestVerdict_String_Held(t *testing.T) {
	v := localloop.Verdict{Parent: local.ResolveParent("1700", ""), Kind: localloop.VerdictHeld, Held: "open seam #45"}
	want := "surface: 1700 held — open seam #45"
	if got := v.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
