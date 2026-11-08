package settle

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/usage"
)

func budgetConfig(maxFixAttempts, maxBudgetTokens int, maxBudgetUSD float64) Config {
	c := fixConfig(maxFixAttempts)
	c.MaxBudgetTokens = maxBudgetTokens
	c.MaxBudgetUSD = maxBudgetUSD
	return c
}

// Once cumulative usage meets MaxBudgetTokens, selfHealGate stops before
// dispatching another fix pass even though MaxFixAttempts alone would still
// allow one, and lands failed with a budget-exhausted status (issue #2001).
func TestSelfHeal_BudgetExhaustedTokens_StopsBeforeFixPass(t *testing.T) {
	c := budgetConfig(3, 100, 0)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := newTestSettle(c, fc, fc)

	d := dispatch.NewFake()
	d.CumulativeUsageResult = usage.Usage{InputTokens: 150}
	landing, reason, _ := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed (budget exhausted)", landing)
	}
	if !strings.Contains(reason, "budget-exhausted:") {
		t.Errorf("selfHeal reason = %q, want a substring containing %q", reason, "budget-exhausted:")
	}
	if len(d.FixCalls) != 0 {
		t.Errorf("expected no fix calls once over budget, got %+v", d.FixCalls)
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Failed {
		t.Errorf("last transition To=%v, want Failed", last.To)
	}
	if len(fc.CommentCalls) == 0 {
		t.Fatal("expected an explanatory issue comment on budget exhaustion")
	}
}

// MaxBudgetUSD alone trips the gate with MaxBudgetTokens unset. The two
// dimensions are independent, and either can exhaust first (issue #2001).
func TestSelfHeal_BudgetExhaustedUSD_StopsBeforeFixPass(t *testing.T) {
	c := budgetConfig(3, 0, 1.00)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := newTestSettle(c, fc, fc)

	d := dispatch.NewFake()
	d.CumulativeUsageResult = usage.Usage{TotalCostUSD: 4.44}
	landing, reason, _ := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed (budget exhausted)", landing)
	}
	if !strings.Contains(reason, "budget-exhausted:") {
		t.Errorf("selfHeal reason = %q, want a substring containing %q", reason, "budget-exhausted:")
	}
	if len(d.FixCalls) != 0 {
		t.Errorf("expected no fix calls once over budget, got %+v", d.FixCalls)
	}
}

// Below both caps, selfHealGate dispatches the fix pass normally (issue #2001).
func TestSelfHeal_UnderBudget_FixProceeds(t *testing.T) {
	c := budgetConfig(3, 1000, 10.00)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	d := dispatch.NewFake()
	d.CumulativeUsageResult = usage.Usage{InputTokens: 100, TotalCostUSD: 0.50}
	landing, _, _ := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged (under budget)", landing)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("expected exactly 1 fix call (under budget), got %+v", d.FixCalls)
	}
}

// With both budget knobs at zero, selfHealGate never stops a fix pass over
// CumulativeUsage's numbers, no matter how large this test makes them (issue
// #2001).
func TestSelfHeal_BudgetUnset_NoEnforcement(t *testing.T) {
	c := fixConfig(2) // MaxBudgetTokens/MaxBudgetUSD left zero
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	d := dispatch.NewFake()
	d.CumulativeUsageResult = usage.Usage{InputTokens: 999999999, TotalCostUSD: 999999.0}
	landing, _, _ := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged (no budget cap set)", landing)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("expected exactly 1 fix call (no budget cap set), got %+v", d.FixCalls)
	}
}
