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
		Strategy.NewLedger != nil {
		t.Error("git is a code-forge-only backend")
	}
}

func TestStrategyValidateCodeForge(t *testing.T) {
	err := Strategy.ValidateCodeForge(backendkit.Shared{}, mapLookup(nil))
	if err == nil || !strings.Contains(err.Error(), "--remote-url") {
		t.Fatalf("err = %v, want one naming --remote-url", err)
	}
	l := mapLookup(map[string]string{"CODE_FORGE_REMOTE_URL": "https://x/y.git"})
	if err := Strategy.ValidateCodeForge(backendkit.Shared{}, l); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestStrategyHasNoWebURL(t *testing.T) {
	if Strategy.WebURL != nil {
		t.Error("git has no web URL")
	}
}

func TestStrategyRemoteMatch(t *testing.T) {
	for _, tc := range []struct {
		name, want, remote string
		match              bool
	}{
		{"raw", "https://git.example.com/team/proj.git", "https://git.example.com/team/proj.git", true},
		{"no trailing .git", "https://git.example.com/team/proj.git", "https://git.example.com/team/proj", true},
		{"mismatch", "https://git.example.com/team/proj.git", "https://git.example.com/team/other.git", false},
		{"scp vs ssh://", "ssh://git@host/owner/repo.git", "git@host:owner/repo.git", true},
		// Both parse to ("", ""); without the non-empty guard they would match.
		{"unparseable local paths", "/srv/git/target.git", "/srv/git/consumer.git", false},
		{"blank remote url", "", "https://git.example.com/team/proj.git", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := mapLookup(map[string]string{"CODE_FORGE_REMOTE_URL": tc.want})
			if got := Strategy.RemoteMatch(backendkit.Shared{}, l, tc.remote); got != tc.match {
				t.Errorf("RemoteMatch(%q) = %v, want %v", tc.remote, got, tc.match)
			}
		})
	}
}

func TestStrategyKnobsFromRemote(t *testing.T) {
	got := Strategy.KnobsFromRemote("git@example.com:o/r.git")
	want := map[string]string{"CODE_FORGE_REMOTE_URL": "git@example.com:o/r.git"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("KnobsFromRemote = %v, want %v", got, want)
	}
	if got := Strategy.KnobsFromRemote(""); got != nil {
		t.Errorf("KnobsFromRemote(\"\") = %v, want nil", got)
	}
}
