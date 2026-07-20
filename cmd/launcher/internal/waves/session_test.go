package waves

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestRunContinuous_NilSession_FallsBackToFixedLimiter verifies that a nil
// *Session — every headless dispatch call site — still runs with a fixed
// limiter built from cfg.MaxParallel, matching the pre-#1547 behaviour of a
// zero-value Config.Limiter.
func TestRunContinuous_NilSession_FallsBackToFixedLimiter(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh); err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
}
