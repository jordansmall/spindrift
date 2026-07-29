package forge_test

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestFake_LandingContained_ScriptsPerLandingAndParent verifies
// SetLandingContained scripts a result keyed by the (landing string, parent)
// pair, defaulting to contained=false, nil when unscripted — the same
// "stays open" default the three predecessors this issue collapsed used.
func TestFake_LandingContained_ScriptsPerLandingAndParent(t *testing.T) {
	f := forge.NewFake()
	f.SetLandingContained("agent/issue-42", "1694", true, nil)
	cf := f.AsLocal()
	query, ok := cf.(forge.LandingContainmentQuery)
	if !ok {
		t.Fatal("AsLocal() does not implement forge.LandingContainmentQuery")
	}

	landing := forge.Landing{Kind: forge.LandingBranchRef, Branch: "agent/issue-42"}
	contained, err := query.LandingContained(landing, forge.NewSeedScope("1694", "integration/1694"))
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if !contained {
		t.Error("LandingContained(scripted true) = false, want true")
	}

	contained, err = query.LandingContained(landing, forge.NewSeedScope("9999", "integration/9999"))
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if contained {
		t.Error("LandingContained(unscripted parent) = true, want false (default)")
	}

	if len(f.LandingContainedCalls) != 2 {
		t.Errorf("LandingContainedCalls = %v, want 2 entries", f.LandingContainedCalls)
	}
}

// TestFake_LandingContained_KeysOnLandingStringForm verifies LandingContained
// scripts against landing.String() — the IntegrationRef grammar
// ("<branch>@<sha>") — so a script written for one landing reference resolves
// correctly when the caller supplies the equivalent typed forge.Landing.
func TestFake_LandingContained_KeysOnLandingStringForm(t *testing.T) {
	f := forge.NewFake()
	f.SetLandingContained("integration/1694@abc123", "1694", true, nil)
	cf := f.AsLocal()
	query := cf.(forge.LandingContainmentQuery)

	integrationRef := forge.Landing{Kind: forge.LandingIntegrationRef, Branch: "integration/1694", SHA: "abc123"}
	contained, err := query.LandingContained(integrationRef, forge.NewSeedScope("1694", "integration/1694"))
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if !contained {
		t.Error("LandingContained(scripted IntegrationRef) = false, want true")
	}

	unscripted := forge.Landing{Kind: forge.LandingIntegrationRef, Branch: "integration/1694", SHA: "def456"}
	contained, err = query.LandingContained(unscripted, forge.NewSeedScope("1694", "integration/1694"))
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if contained {
		t.Error("LandingContained(unscripted sha) = true, want false")
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
