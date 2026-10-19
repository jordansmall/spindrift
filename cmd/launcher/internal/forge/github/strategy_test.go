package github

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		Strategy.NewReadOnlyCodeForge == nil || Strategy.NewLedger == nil || Strategy.OpenPRForBranch == nil {
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

func TestStrategyCloneURLIsLiteralGithubCom(t *testing.T) {
	t.Setenv("GH_HOST", "ghe.example.com")
	got, err := Strategy.CloneURL(backendkit.Shared{RepoSlug: "o/r"}, nil, "")
	if err != nil || got != "https://github.com/o/r.git" {
		t.Errorf("CloneURL = %q, %v, want https://github.com/o/r.git", got, err)
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

func TestStrategyOpenPRForBranchPassesSlugAndBranchThrough(t *testing.T) {
	dir := prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  echo "https://github.com/o/r/pull/7"
  exit 0
fi
exit 1
`)
	open, err := Strategy.OpenPRForBranch("o/r", "agent/issue-7")
	if err != nil || !open {
		t.Fatalf("OpenPRForBranch = (%v, %v), want (true, nil)", open, err)
	}
	argv, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"o/r", "agent/issue-7"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("gh argv %q lacks %q", argv, want)
		}
	}
}

func TestStrategyOpenPRForBranchPropagatesError(t *testing.T) {
	prependFakeGH(t, "echo 'HTTP 502: Bad Gateway' >&2\nexit 1\n")
	open, err := Strategy.OpenPRForBranch("o/r", "agent/issue-7")
	if err == nil || open {
		t.Fatalf("OpenPRForBranch = (%v, %v), want (false, error)", open, err)
	}
}
