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
	if Strategy.ValidateTracker == nil {
		t.Error("github must validate its tracker axis (REPO_SLUG)")
	}
	if Strategy.ValidateCodeForge != nil {
		t.Error("github declares no code-forge validator")
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

func TestStrategyValidateTrackerRequiresSlug(t *testing.T) {
	err := Strategy.ValidateTracker(backendkit.Shared{}, nil)
	if err == nil || err.Error() != "set REPO_SLUG when ISSUE_TRACKER=github" {
		t.Errorf("err = %v, want the REPO_SLUG message", err)
	}
	if err := Strategy.ValidateTracker(backendkit.Shared{RepoSlug: "o/r"}, nil); err != nil {
		t.Errorf("err = %v, want nil with a slug", err)
	}
}
