package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/usage"
)

// budgetConfig returns a fixConfig with a token/cost budget layered on top.
func budgetConfig(maxFixAttempts, maxBudgetTokens int, maxBudgetUSD float64) Config {
	c := fixConfig(maxFixAttempts)
	c.MaxBudgetTokens = maxBudgetTokens
	c.MaxBudgetUSD = maxBudgetUSD
	return c
}

// TestSelfHeal_BudgetExhaustedTokens_StopsBeforeFixPass verifies that when
// cumulative usage already meets or exceeds MaxBudgetTokens, selfHealGate
// stops before dispatching another fix pass — even though MaxFixAttempts
// alone would still allow one — and lands failed with a distinct
// budget-exhausted status (issue #2001).
func TestSelfHeal_BudgetExhaustedTokens_StopsBeforeFixPass(t *testing.T) {
	c := budgetConfig(3, 100, 0)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	d.CumulativeUsageResult = usage.Usage{InputTokens: 150}
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed (budget exhausted)", landing)
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

// TestSelfHeal_BudgetExhaustedUSD_StopsBeforeFixPass mirrors the token-cap
// test for the cost dimension: MaxBudgetUSD alone can trip the gate even
// with MaxBudgetTokens unset (issue #2001) — the two dimensions are
// independent, either can exhaust first.
func TestSelfHeal_BudgetExhaustedUSD_StopsBeforeFixPass(t *testing.T) {
	c := budgetConfig(3, 0, 1.00)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	d.CumulativeUsageResult = usage.Usage{TotalCostUSD: 4.44}
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed (budget exhausted)", landing)
	}
	if len(d.FixCalls) != 0 {
		t.Errorf("expected no fix calls once over budget, got %+v", d.FixCalls)
	}
}

// TestSelfHeal_UnderBudget_FixProceeds verifies that when cumulative usage
// is below both caps, selfHealGate dispatches the fix pass normally (issue
// #2001 acceptance criterion: under budget → fix proceeds).
func TestSelfHeal_UnderBudget_FixProceeds(t *testing.T) {
	c := budgetConfig(3, 1000, 10.00)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	d.CumulativeUsageResult = usage.Usage{InputTokens: 100, TotalCostUSD: 0.50}
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged (under budget)", landing)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("expected exactly 1 fix call (under budget), got %+v", d.FixCalls)
	}
}

// TestSelfHeal_BudgetUnset_NoEnforcement verifies that with both budget
// knobs left at their zero value, behaviour is unchanged: selfHealGate never
// consults CumulativeUsage's numbers to stop a fix pass, even when they are
// scripted enormous (issue #2001 acceptance criterion: unset = no cap).
func TestSelfHeal_BudgetUnset_NoEnforcement(t *testing.T) {
	c := fixConfig(2) // MaxBudgetTokens/MaxBudgetUSD left zero
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	d.CumulativeUsageResult = usage.Usage{InputTokens: 999999999, TotalCostUSD: 999999.0}
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged (no budget cap set)", landing)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("expected exactly 1 fix call (no budget cap set), got %+v", d.FixCalls)
	}
}

// TestBudgetExceeded_TableDriven unit-tests budgetExceeded's cap logic
// directly, independent of selfHealGate's loop plumbing (issue #2001
// acceptance criterion: usage summing/threshold logic is unit-tested).
func TestBudgetExceeded_TableDriven(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		usage    usage.Usage
		wantOver bool
	}{
		{"both zero: never exceeded", Config{}, usage.Usage{InputTokens: 1_000_000, TotalCostUSD: 1_000_000}, false},
		{"tokens under cap", Config{MaxBudgetTokens: 100}, usage.Usage{InputTokens: 99}, false},
		{"tokens at cap", Config{MaxBudgetTokens: 100}, usage.Usage{InputTokens: 100}, true},
		{"tokens over cap, summed across fields", Config{MaxBudgetTokens: 100}, usage.Usage{InputTokens: 30, OutputTokens: 30, CacheReadInputTokens: 30, CacheCreationInputTokens: 30}, true},
		{"cost under cap", Config{MaxBudgetUSD: 5}, usage.Usage{TotalCostUSD: 4.99}, false},
		{"cost at cap", Config{MaxBudgetUSD: 5}, usage.Usage{TotalCostUSD: 5}, true},
		{"tokens fine but cost over", Config{MaxBudgetTokens: 1000, MaxBudgetUSD: 5}, usage.Usage{InputTokens: 10, TotalCostUSD: 6}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOver, reason := budgetExceeded(tc.cfg, tc.usage)
			if gotOver != tc.wantOver {
				t.Errorf("budgetExceeded = %v (%q), want over=%v", gotOver, reason, tc.wantOver)
			}
			if gotOver && reason == "" {
				t.Error("expected a non-empty reason when exceeded")
			}
		})
	}
}
