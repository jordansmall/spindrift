package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
)

// A handshake declaring push-only resolves to a push-only forge, and the
// push-only Lander settles through it: the merge runs in core's git plumbing
// against a real bare repo, not through a PR.
func TestSettle_PushOnlyLanderThroughConnectorForge(t *testing.T) {
	h := connectorforgetest.New(t)
	h.SeedLandable("1")

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	cfg := baseConfig()
	cfg.Capabilities = h.Capabilities()
	s := New(cfg, fc, h.Forge())

	if _, ok := s.lander.(pushOnlyLander); !ok {
		t.Fatalf("lander = %T, want pushOnlyLander for a push-only handshake", s.lander)
	}

	gen := s.term.Begin("1")
	got := s.Settle(dispatch.NewFake(), "1", gen, settleReadyResult("1", h.BranchPrefix()+"1"))

	if want := (Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged)}); got != want {
		t.Errorf("Settle = %+v, want %+v", got, want)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete; labels=%v", iss.Labels)
	}
	if !h.Landed("1") {
		t.Error("agent branch did not reach main on the bare repo")
	}
}
