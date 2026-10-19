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
		s.NewCodeForge == nil || s.NewReadOnlyCodeForge == nil || s.NewLedger == nil ||
		s.ConfigureCLI == nil {
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

func TestStrategyCloneURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default base", map[string]string{"FORGEJO_TOKEN": "t"}, "https://t@codeberg.org/o/r.git"},
		{"trims slash", map[string]string{"FORGEJO_TOKEN": "t", "FORGEJO_BASE_URL": "http://f.test:3000/"}, "http://t@f.test:3000/o/r.git"},
		{"schemeless base", map[string]string{"FORGEJO_TOKEN": "t", "FORGEJO_BASE_URL": "f.test"}, "f.test://t@f.test/o/r.git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := forgejo.Strategy.CloneURL(backendkit.Shared{RepoSlug: "o/r"}, mapLookup(tc.env), "")
			if err != nil || got != tc.want {
				t.Errorf("CloneURL = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestStrategyCloneURLRequiresToken(t *testing.T) {
	_, err := forgejo.Strategy.CloneURL(backendkit.Shared{RepoSlug: "o/r"}, mapLookup(nil), "")
	if err == nil || !strings.Contains(err.Error(), "FORGEJO_TOKEN is required when CODE_FORGE=forgejo") {
		t.Errorf("err = %v, want one naming FORGEJO_TOKEN", err)
	}
}

func TestStrategyWebURL(t *testing.T) {
	for _, tc := range []struct {
		name, slug, want string
		env              map[string]string
	}{
		{"default base", "o/r", "https://codeberg.org/o/r", nil},
		{"trailing slash", "o/r", "https://git.example.com/o/r", map[string]string{"FORGEJO_BASE_URL": "https://git.example.com/"}},
		{"empty slug", "", "", nil},
		// The launcher rejects an explicitly blank base, so the link is
		// withheld rather than guessed; unset (above) takes the default.
		{"explicit blank base", "o/r", "", map[string]string{"FORGEJO_BASE_URL": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := forgejo.Strategy.WebURL(backendkit.Shared{RepoSlug: tc.slug}, mapLookup(tc.env)); got != tc.want {
				t.Errorf("WebURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStrategyRemoteMatch(t *testing.T) {
	for _, tc := range []struct {
		name, slug, remote string
		env                map[string]string
		want               bool
	}{
		{"default base", "owner/repo", "git@codeberg.org:owner/repo.git", nil, true},
		{"explicit base", "owner/repo", "git@codeberg.org:owner/repo.git", map[string]string{"FORGEJO_BASE_URL": "https://codeberg.org"}, true},
		{"case-insensitive", "owner/repo", "git@CODEBERG.ORG:Owner/Repo.git", nil, true},
		{"host mismatch", "owner/repo", "git@git.example.com:owner/repo.git", nil, false},
		{"slug mismatch", "owner/repo", "git@codeberg.org:owner/other.git", nil, false},
		{"custom base", "owner/repo", "https://git.example.com/owner/repo.git", map[string]string{"FORGEJO_BASE_URL": "https://git.example.com/"}, true},
		{"empty slug", "", "git@codeberg.org:owner/repo.git", nil, false},
		{"explicit blank base", "owner/repo", "git@codeberg.org:owner/repo.git", map[string]string{"FORGEJO_BASE_URL": ""}, false},
		{"hostless base", "owner/repo", "git@codeberg.org:owner/repo.git", map[string]string{"FORGEJO_BASE_URL": "not a url"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := forgejo.Strategy.RemoteMatch(backendkit.Shared{RepoSlug: tc.slug}, mapLookup(tc.env), tc.remote); got != tc.want {
				t.Errorf("RemoteMatch(%q) = %v, want %v", tc.remote, got, tc.want)
			}
		})
	}
}

// ReadKnobs re-defaults a blank FORGEJO_BASE_URL to codeberg, which would send
// a self-hosted FORGEJO_TOKEN there; validation reads the raw lookup value and
// fails closed instead, on both axes.
func TestStrategyValidateRejectsBlankBaseURL(t *testing.T) {
	l := mapLookup(map[string]string{"FORGEJO_BASE_URL": "", "FORGEJO_TOKEN": "tok"})
	for axis, validate := range map[string]func(backendkit.Shared, backendkit.Lookup) error{
		"ISSUE_TRACKER": forgejo.Strategy.ValidateTracker,
		"CODE_FORGE":    forgejo.Strategy.ValidateCodeForge,
	} {
		err := validate(backendkit.Shared{RepoSlug: "o/r"}, l)
		if err == nil || !strings.Contains(err.Error(), "FORGEJO_BASE_URL") || !strings.Contains(err.Error(), axis+"=forgejo") {
			t.Errorf("%s: err = %v, want one naming FORGEJO_BASE_URL and %s=forgejo", axis, err, axis)
		}
	}
}
