package trackerbuild

import (
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
)

func TestByName(t *testing.T) {
	for _, d := range backend.Registry {
		_, ok := ByName(d.Name)
		if ok != d.ValidAsTracker {
			t.Errorf("ByName(%q) ok = %v, want %v (ValidAsTracker)", d.Name, ok, d.ValidAsTracker)
		}
	}
	for _, name := range []string{"", "git", "nope"} {
		if _, ok := ByName(name); ok {
			t.Errorf("ByName(%q) resolved, want unknown", name)
		}
	}
}

func TestValidate(t *testing.T) {
	full := Settings{
		RepoSlug:       "o/r",
		LocalIssuesDir: "/tmp/issues",
		ForgejoBaseURL: "https://f.example",
		ForgejoToken:   "t",
		JiraBaseURL:    "https://j.example",
		JiraProjectKey: "K",
		JiraToken:      "t",
	}
	tests := []struct {
		name    string
		tracker Tracker
		mutate  func(*Settings)
		wantErr bool
	}{
		{"github complete", GitHub, func(*Settings) {}, false},
		{"github no slug", GitHub, func(s *Settings) { s.RepoSlug = "" }, true},
		{"forgejo complete", Forgejo, func(*Settings) {}, false},
		{"forgejo no slug", Forgejo, func(s *Settings) { s.RepoSlug = "" }, true},
		{"forgejo no base url", Forgejo, func(s *Settings) { s.ForgejoBaseURL = "" }, true},
		{"forgejo no token", Forgejo, func(s *Settings) { s.ForgejoToken = "" }, true},
		{"jira complete", Jira, func(*Settings) {}, false},
		{"jira no base url", Jira, func(s *Settings) { s.JiraBaseURL = "" }, true},
		{"jira no project", Jira, func(s *Settings) { s.JiraProjectKey = "" }, true},
		{"jira no token", Jira, func(s *Settings) { s.JiraToken = "" }, true},
		{"jira bad mapping", Jira, func(s *Settings) { s.JiraStatusMapping = "garbage" }, true},
		{"local complete", Local, func(*Settings) {}, false},
		{"local no dir", Local, func(s *Settings) { s.LocalIssuesDir = "" }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := full
			tc.mutate(&s)
			err := tc.tracker.Validate(s)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestNew(t *testing.T) {
	s := Settings{
		RepoSlug:          "o/r",
		LocalIssuesDir:    t.TempDir(),
		ForgejoBaseURL:    "https://f.example",
		ForgejoToken:      "t",
		JiraBaseURL:       "https://j.example",
		JiraProjectKey:    "K",
		JiraToken:         "t",
		JiraStatusMapping: "garbage", // New falls back to an unmapped lifecycle
	}
	for _, d := range backend.Registry {
		tr, ok := ByName(d.Name)
		if !ok {
			continue
		}
		t.Run(d.Name, func(t *testing.T) {
			it := tr.New(s)
			if it == nil {
				t.Fatal("New returned nil")
			}
			if _, ok := it.(forge.DemandCounter); !ok {
				t.Errorf("%T does not implement forge.DemandCounter", it)
			}
		})
	}
}
