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

func TestStrategyCloneURL(t *testing.T) {
	got, err := Strategy.CloneURL(backendkit.Shared{}, mapLookup(map[string]string{"CODE_FORGE_REMOTE_URL": "ssh://h/x.git"}), "")
	if err != nil || got != "ssh://h/x.git" {
		t.Errorf("CloneURL = %q, %v, want ssh://h/x.git", got, err)
	}
	for _, l := range []backendkit.Lookup{mapLookup(nil), mapLookup(map[string]string{"CODE_FORGE_REMOTE_URL": ""})} {
		_, err := Strategy.CloneURL(backendkit.Shared{}, l, "")
		if err == nil || !strings.Contains(err.Error(), "CODE_FORGE_REMOTE_URL is required when CODE_FORGE=git") {
			t.Errorf("err = %v, want one naming CODE_FORGE_REMOTE_URL", err)
		}
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
