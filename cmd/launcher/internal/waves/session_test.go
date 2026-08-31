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
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	discover := func() (Batch, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return Batch{}, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return Batch{Issues: out, Edges: map[string][]string{}}, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, dir, f, s, QueueFromDiscoverer(discover), fresh); err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
}
