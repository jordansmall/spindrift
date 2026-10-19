package forgejo_test

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge/forgejo"
)

func mapLookup(m map[string]string) backendkit.Lookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestStrategyShape(t *testing.T) {
	s := forgejo.Strategy
	if !reflect.DeepEqual(s.Descriptor, backend.Forgejo) {
		t.Errorf("Descriptor = %+v, want backend.Forgejo", s.Descriptor)
	}
	if s.ValidateTracker == nil || s.ValidateCodeForge == nil || s.NewIssueTracker == nil ||
		s.NewCodeForge == nil || s.NewReadOnlyCodeForge == nil || s.NewLedger == nil {
		t.Error("forgejo must fill every closure")
	}
}

func TestStrategyValidateTrackerNamesMissingToken(t *testing.T) {
	err := forgejo.Strategy.ValidateTracker(backendkit.Shared{}, mapLookup(nil))
	if err == nil || !strings.Contains(err.Error(), "FORGEJO_TOKEN") ||
		!strings.Contains(err.Error(), "ISSUE_TRACKER=forgejo") {
		t.Errorf("err = %v, want one naming FORGEJO_TOKEN and ISSUE_TRACKER=forgejo", err)
	}
}

func TestStrategyLedgerURLUsesLookupBaseURL(t *testing.T) {
	l := mapLookup(map[string]string{"FORGEJO_BASE_URL": "https://forge.example", "FORGEJO_TOKEN": "tok"})
	site, err := forgejo.Strategy.NewLedger(backendkit.Shared{RepoSlug: "o/r"}, l)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(site.URL, "forge.example") || !strings.Contains(site.URL, "o/r") {
		t.Errorf("URL = %q, want it built from the looked-up base URL and slug", site.URL)
	}
}

func TestStrategyValidateTrackerRequiresSlug(t *testing.T) {
	l := mapLookup(map[string]string{"FORGEJO_BASE_URL": "https://forge.example", "FORGEJO_TOKEN": "tok"})
	err := forgejo.Strategy.ValidateTracker(backendkit.Shared{}, l)
	if err == nil || err.Error() != "set REPO_SLUG when ISSUE_TRACKER=forgejo" {
		t.Errorf("err = %v, want the REPO_SLUG message", err)
	}
	if err := forgejo.Strategy.ValidateTracker(backendkit.Shared{RepoSlug: "o/r"}, l); err != nil {
		t.Errorf("err = %v, want nil with a slug", err)
	}
}

func TestStrategyValidateCodeForgeIgnoresSlug(t *testing.T) {
	l := mapLookup(map[string]string{"FORGEJO_BASE_URL": "https://forge.example", "FORGEJO_TOKEN": "tok"})
	if err := forgejo.Strategy.ValidateCodeForge(backendkit.Shared{}, l); err != nil {
		t.Errorf("err = %v, want nil: the slug is a tracker-axis requirement", err)
	}
}
