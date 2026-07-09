package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestNewComposite_ImplementsClient asserts that composing an IssueTracker
// with a CodeForge satisfies the combined Client interface.
func TestNewComposite_ImplementsClient(t *testing.T) {
	var _ forge.Client = forge.NewComposite(forge.NewFake(), forge.NewFake())
}

// TestNewComposite_DelegatesToEachSeam verifies that IssueTracker calls reach
// the tracker and CodeForge calls reach the code forge — the two seams stay
// independently swappable (ADR 0013).
func TestNewComposite_DelegatesToEachSeam(t *testing.T) {
	it := forge.NewFake()
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{"ready-for-agent"}})
	cf := forge.NewFake()
	cf.AutoMergeAllowed = true

	c := forge.NewComposite(it, cf)

	iss, err := c.Issue("1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.Number != "1" {
		t.Errorf("Issue routed to the wrong seam: %+v", iss)
	}

	ok, err := c.CanAutoMerge()
	if err != nil || !ok {
		t.Errorf("CanAutoMerge = (%v, %v), want (true, nil) — CodeForge call must route to cf", ok, err)
	}
}
