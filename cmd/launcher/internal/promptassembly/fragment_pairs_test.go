package promptassembly

import (
	"strings"
	"testing"
)

// Pins one Gates-computed pair, TDD_BAKED/TDD_UNBAKED: it never renders both
// members or neither, so flipping the knob swaps which body renders. Go has no
// general inverseOf mechanic, and whether a registry's declared pairs are
// well-formed is lib/fragment-pairs.nix's check, not this one.
func TestTDDGatePairRendersExactlyOneMember(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "TDD_BAKED", Fragment: "baked.md", Var: "BAKED_STEP"},
			{Gate: "TDD_UNBAKED", Fragment: "unbaked.md", Var: "UNBAKED_STEP"},
		},
		map[string]string{
			"issue-prompt.md":      "[${BAKED_STEP}|${UNBAKED_STEP}]\n",
			"fragments/baked.md":   "on-body",
			"fragments/unbaked.md": "off-body",
		})

	var sawBaked, sawUnbaked bool
	for _, baked := range []bool{true, false} {
		env.TDDSkillBaked = baked
		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble(TDDSkillBaked=%v): %v", baked, err)
		}
		hasBaked := strings.Contains(result.Prompt, "on-body")
		hasUnbaked := strings.Contains(result.Prompt, "off-body")
		if hasBaked == hasUnbaked {
			t.Fatalf("TDDSkillBaked=%v: want exactly one member rendered, Prompt:\n%s", baked, result.Prompt)
		}
		sawBaked = sawBaked || hasBaked
		sawUnbaked = sawUnbaked || hasUnbaked
	}
	// Both arms must be observed, or the assertion above passes vacuously on a
	// knob that never moves the pair.
	if !sawBaked || !sawUnbaked {
		t.Errorf("sawBaked=%v sawUnbaked=%v, want both arms rendered across the knob", sawBaked, sawUnbaked)
	}
}
