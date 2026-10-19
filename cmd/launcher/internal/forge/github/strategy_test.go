package github

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
)

func TestStrategyShape(t *testing.T) {
	if !reflect.DeepEqual(Strategy.Descriptor, backend.GitHub) {
		t.Errorf("Descriptor = %+v, want backend.GitHub", Strategy.Descriptor)
	}
	if Strategy.ValidateTracker != nil || Strategy.ValidateCodeForge != nil {
		t.Error("github declares no validators")
	}
	if Strategy.NewIssueTracker == nil || Strategy.NewCodeForge == nil ||
		Strategy.NewReadOnlyCodeForge == nil || Strategy.NewLedger == nil {
		t.Error("github must fill every constructor")
	}
}

func TestStrategyLedgerIsGitRemote(t *testing.T) {
	site, err := Strategy.NewLedger(backendkit.Shared{RepoSlug: "o/r"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	url, args := GitRemote("o/r")
	if site.URL != url || !reflect.DeepEqual(site.GitArgs, args) {
		t.Errorf("site = %+v, want %q %v", site, url, args)
	}
}
