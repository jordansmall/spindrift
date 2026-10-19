package local

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
	if !reflect.DeepEqual(Strategy.Descriptor, backend.Local) {
		t.Errorf("Descriptor = %+v, want backend.Local", Strategy.Descriptor)
	}
	if Strategy.ValidateCodeForge == nil || Strategy.NewIssueTracker == nil ||
		Strategy.NewCodeForge == nil || Strategy.NewLedger == nil {
		t.Error("local must fill ValidateCodeForge, NewIssueTracker, NewCodeForge, NewLedger")
	}
	if Strategy.ValidateTracker != nil || Strategy.NewReadOnlyCodeForge != nil {
		t.Error("local has no tracker validator and no read-only forge")
	}
}

func TestStrategyValidateCodeForgeRequiresImmediate(t *testing.T) {
	err := Strategy.ValidateCodeForge(backendkit.Shared{MergeMode: "manual"}, nil)
	if err == nil || !strings.Contains(err.Error(), "MERGE_MODE=immediate") {
		t.Errorf("manual: err = %v, want MERGE_MODE=immediate complaint", err)
	}
	if err := Strategy.ValidateCodeForge(backendkit.Shared{MergeMode: "immediate"}, nil); err != nil {
		t.Errorf("immediate: err = %v, want nil", err)
	}
}

func TestStrategyLedgerReadsAccumulationRepo(t *testing.T) {
	l := mapLookup(map[string]string{"CODE_FORGE_ACCUMULATION_REPO_DIR": "/acc"})
	site, err := Strategy.NewLedger(backendkit.Shared{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if site.Repo != "/acc" || site.URL != "" {
		t.Errorf("site = %+v, want Repo=/acc", site)
	}
}

func TestStrategyCloneURLIsRepoMountDir(t *testing.T) {
	got, err := Strategy.CloneURL(backendkit.Shared{}, nil, "/mnt/repo")
	if err != nil || got != "/mnt/repo" {
		t.Errorf("CloneURL = %q, %v, want /mnt/repo", got, err)
	}
}

func TestStrategyHasNoWebURLOrRemoteMatch(t *testing.T) {
	if Strategy.WebURL != nil || Strategy.RemoteMatch != nil {
		t.Error("local has no web URL and never matches a remote")
	}
}
