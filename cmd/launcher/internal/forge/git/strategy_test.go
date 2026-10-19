package git

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
)

func mapLookup(m map[string]string) backendkit.Lookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestStrategyShape(t *testing.T) {
	if !reflect.DeepEqual(Strategy.Descriptor, backend.Git) {
		t.Errorf("Descriptor = %+v, want backend.Git", Strategy.Descriptor)
	}
	if Strategy.ValidateCodeForge == nil || Strategy.NewCodeForge == nil {
		t.Error("git must fill ValidateCodeForge and NewCodeForge")
	}
	if Strategy.ValidateTracker != nil || Strategy.NewIssueTracker != nil ||
		Strategy.NewReadOnlyCodeForge != nil || Strategy.NewLedger != nil {
		t.Error("git is a code-forge-only backend")
	}
}

func TestStrategyValidateCodeForge(t *testing.T) {
	err := Strategy.ValidateCodeForge(backendkit.Shared{}, mapLookup(nil))
	if err == nil || !strings.Contains(err.Error(), "CODE_FORGE_REMOTE_URL") {
		t.Fatalf("err = %v, want one naming CODE_FORGE_REMOTE_URL", err)
	}
	l := mapLookup(map[string]string{"CODE_FORGE_REMOTE_URL": "https://x/y.git"})
	if err := Strategy.ValidateCodeForge(backendkit.Shared{}, l); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}
