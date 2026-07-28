package forge_test

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestFake_BranchMergedIntoIntegration_ScriptsPerBranchAndParent verifies
// SetBranchMergedIntoIntegration scripts a result keyed by the (branch,
// parent) pair, defaulting to merged=false, nil when unscripted — the same
// "stays open" default SetVerifyLanding uses.
func TestFake_BranchMergedIntoIntegration_ScriptsPerBranchAndParent(t *testing.T) {
	f := forge.NewFake()
	f.SetBranchMergedIntoIntegration("agent/issue-42", "1694", true, nil)
	cf := f.AsLocal()
	repair, ok := cf.(forge.LandingRepair)
	if !ok {
		t.Fatal("AsLocal() does not implement forge.LandingRepair")
	}

	merged, err := repair.BranchMergedIntoIntegration("agent/issue-42", "1694")
	if err != nil {
		t.Fatalf("BranchMergedIntoIntegration: %v", err)
	}
	if !merged {
		t.Error("BranchMergedIntoIntegration(scripted true) = false, want true")
	}

	merged, err = repair.BranchMergedIntoIntegration("agent/issue-42", "9999")
	if err != nil {
		t.Fatalf("BranchMergedIntoIntegration: %v", err)
	}
	if merged {
		t.Error("BranchMergedIntoIntegration(unscripted parent) = true, want false (default)")
	}

	if len(f.BranchMergedIntoIntegrationCalls) != 2 {
		t.Errorf("BranchMergedIntoIntegrationCalls = %v, want 2 entries", f.BranchMergedIntoIntegrationCalls)
	}
}

// TestFake_IntegrationTip_ScriptsPerParent verifies SetIntegrationTip scripts
// IntegrationTip's success result per parent, and IntegrationTipErr overrides
// it for every call — mirroring LandingRefErr's precedence over
// LandingRefValue.
func TestFake_IntegrationTip_ScriptsPerParent(t *testing.T) {
	f := forge.NewFake()
	f.SetIntegrationTip("1694", "integration/1694@abc123")
	cf := f.AsLocal()
	repair := cf.(forge.LandingRepair)

	got, err := repair.IntegrationTip("1694")
	if err != nil {
		t.Fatalf("IntegrationTip: %v", err)
	}
	if got != "integration/1694@abc123" {
		t.Errorf("IntegrationTip(1694) = %q, want %q", got, "integration/1694@abc123")
	}

	wantErr := errors.New("local: repo unreadable")
	f.IntegrationTipErr = wantErr
	if _, err := repair.IntegrationTip("1694"); !errors.Is(err, wantErr) {
		t.Errorf("IntegrationTip error = %v, want %v", err, wantErr)
	}
}

// TestFake_IntegrationContainsLanding_ScriptsPerLandingAndParent verifies
// SetIntegrationContainsLanding scripts a result keyed by the (landing,
// parent) pair, defaulting to contained=false, nil when unscripted — the
// same "not contained" default SetBranchMergedIntoIntegration uses.
func TestFake_IntegrationContainsLanding_ScriptsPerLandingAndParent(t *testing.T) {
	f := forge.NewFake()
	f.SetIntegrationContainsLanding("agent/issue-42@abc123", "1694", true, nil)
	cf := f.AsLocal()
	query, ok := cf.(forge.LandingContainmentQuery)
	if !ok {
		t.Fatal("AsLocal() does not implement forge.LandingContainmentQuery")
	}

	contained, err := query.IntegrationContainsLanding("agent/issue-42@abc123", "1694")
	if err != nil {
		t.Fatalf("IntegrationContainsLanding: %v", err)
	}
	if !contained {
		t.Error("IntegrationContainsLanding(scripted true) = false, want true")
	}

	contained, err = query.IntegrationContainsLanding("agent/issue-99@def456", "9999")
	if err != nil {
		t.Fatalf("IntegrationContainsLanding: %v", err)
	}
	if contained {
		t.Error("IntegrationContainsLanding(unscripted landing/parent) = true, want false (default)")
	}
}
