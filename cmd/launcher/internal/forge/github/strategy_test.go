package github

import (
	"errors"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
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
		Strategy.NewLedger == nil {
		t.Error("github must fill every constructor")
	}
}

// With no routed row (none applied, a shadow row, or a GitHub Enterprise
// GH_HOST) the Ledger has no remote to name: NewLedger fails with ErrNoRoute
// rather than falling back to a gh credential-helper URL.
func TestStrategyLedgerFailsWithNoRoute(t *testing.T) {
	s := backendkit.Shared{RepoSlug: "o/r"}
	for name, setup := range map[string]func(*testing.T){
		"no row": func(t *testing.T) {
			t.Cleanup(backendrows.Snapshot())
			if err := backendrows.Use([]byte("[]"), func(string) string { return "" }); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GH_HOST", "")
		},
		"shadow row": func(t *testing.T) { useGithubRowShadow(t, "127.0.0.1:1") },
	} {
		t.Run(name, func(t *testing.T) {
			setup(t)
			if _, err := Strategy.NewLedger(s, nil); !errors.Is(err, ErrNoRoute) {
				t.Errorf("NewLedger err = %v, want ErrNoRoute", err)
			}
		})
	}
}

func TestStrategyValidateTrackerRequiresSlug(t *testing.T) {
	err := Strategy.ValidateTracker(backendkit.Shared{}, nil)
	if err == nil || err.Error() != "pass --repo-slug owner/repo or set forge.repoSlug when ISSUE_TRACKER=github" {
		t.Errorf("err = %v, want the repo-slug remedy", err)
	}
	if err := Strategy.ValidateTracker(backendkit.Shared{RepoSlug: "o/r"}, nil); err != nil {
		t.Errorf("err = %v, want nil with a slug", err)
	}
}

func TestStrategyWebURL(t *testing.T) {
	for _, tc := range []struct {
		name, ghHost, slug, want string
	}{
		{"default host", "", "o/r", "https://github.com/o/r"},
		{"GH_HOST", "ghe.example.com", "o/r", "https://ghe.example.com/o/r"},
		{"empty slug", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GH_HOST", tc.ghHost)
			if got := Strategy.WebURL(backendkit.Shared{RepoSlug: tc.slug}, nil); got != tc.want {
				t.Errorf("WebURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStrategyRemoteMatch(t *testing.T) {
	for _, tc := range []struct {
		name, ghHost, slug, remote string
		want                       bool
	}{
		{"scp", "", "owner/repo", "git@github.com:owner/repo.git", true},
		{"https", "", "owner/repo", "https://github.com/owner/repo.git", true},
		{"case-insensitive slug", "", "owner/repo", "git@github.com:Owner/Repo.git", true},
		{"other repo", "", "owner/repo", "git@github.com:other/elsewhere.git", false},
		{"GH_HOST scp", "ghe.example.com", "owner/repo", "git@ghe.example.com:owner/repo.git", true},
		{"GH_HOST https", "ghe.example.com", "owner/repo", "https://ghe.example.com/owner/repo.git", true},
		{"GH_HOST case-insensitive", "GHE.Example.com", "owner/repo", "git@ghe.example.com:owner/repo.git", true},
		{"github.com stops matching under GH_HOST", "ghe.example.com", "owner/repo", "git@github.com:owner/repo.git", false},
		{"empty slug", "", "", "git@github.com:owner/repo.git", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GH_HOST", tc.ghHost)
			if got := Strategy.RemoteMatch(backendkit.Shared{RepoSlug: tc.slug}, nil, tc.remote); got != tc.want {
				t.Errorf("RemoteMatch(%q) = %v, want %v", tc.remote, got, tc.want)
			}
		})
	}
}

// The Box holds no forge client or token, so github recovery is answered by
// the host through BOX_HOST_OPEN_PR; forgejo, local and git are not asked.
func TestStrategyHostAnswersOpenPR(t *testing.T) {
	if !Strategy.HostAnswersOpenPR {
		t.Error("github must have the host answer the Box's open-PR question")
	}
}
