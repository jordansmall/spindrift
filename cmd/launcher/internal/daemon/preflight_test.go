package daemon

import (
	"strconv"
	"strings"
	"testing"
)

func TestClassifyPreflight(t *testing.T) {
	cases := []struct {
		exit        int
		wantOutcome string
		wantHealthy bool
	}{
		{0, "doctor-healthy", true},
		{1, "doctor-unclassified", false},
		{2, "doctor-config-invalid", false},
		{3, "doctor-connectivity", false},
		{4, "doctor-required-labels-missing", false},
		{9, "doctor-unknown", false},
		{-1, "doctor-unknown", false},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.exit), func(t *testing.T) {
			v := ClassifyPreflight(tc.exit)
			if v.Outcome != tc.wantOutcome {
				t.Errorf("ClassifyPreflight(%d).Outcome = %q, want %q", tc.exit, v.Outcome, tc.wantOutcome)
			}
			if v.Healthy != tc.wantHealthy {
				t.Errorf("ClassifyPreflight(%d).Healthy = %v, want %v", tc.exit, v.Healthy, tc.wantHealthy)
			}
			if v.Exit != tc.exit {
				t.Errorf("ClassifyPreflight(%d).Exit = %d, want %d", tc.exit, v.Exit, tc.exit)
			}
			if tc.wantHealthy {
				if v.HaltReason() != "" {
					t.Errorf("ClassifyPreflight(%d).HaltReason() = %q, want empty for a healthy verdict", tc.exit, v.HaltReason())
				}
				return
			}
			if v.Detail == "" {
				t.Errorf("ClassifyPreflight(%d).Detail is empty, want non-empty", tc.exit)
			}
			if v.Remedy == "" {
				t.Errorf("ClassifyPreflight(%d).Remedy is empty, want non-empty", tc.exit)
			}
			reason := v.HaltReason()
			if !strings.HasPrefix(reason, HaltPreflightPrefix) {
				t.Errorf("ClassifyPreflight(%d).HaltReason() = %q, want prefix %q", tc.exit, reason, HaltPreflightPrefix)
			}
			if !strings.Contains(reason, strconv.Itoa(tc.exit)) {
				t.Errorf("ClassifyPreflight(%d).HaltReason() = %q, want it to mention the exit code", tc.exit, reason)
			}
		})
	}
}

// TestClassifyPreflightNeverConflatesWithInterpret is the non-conflation
// guard issue #3544's AC demands: doctor's exit-code table and a dispatched
// child's exit-code table (Interpret) must never converge on a shared
// outcome label, even though both tables index small overlapping integers.
func TestClassifyPreflightNeverConflatesWithInterpret(t *testing.T) {
	for exit := 0; exit <= 7; exit++ {
		preflightOutcome := ClassifyPreflight(exit).Outcome
		childOutcome, _ := Interpret(exit)
		if preflightOutcome == childOutcome {
			t.Errorf("exit %d: ClassifyPreflight outcome %q collides with Interpret outcome %q", exit, preflightOutcome, childOutcome)
		}
	}
}
