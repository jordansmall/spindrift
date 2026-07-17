package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildForge_GithubTracker_ReturnsSameInstanceForBothSeams(t *testing.T) {
	it, cf := buildForge("owner/repo", trackerSettings{issueTracker: "github"}, "ghp_faketoken", "")
	if it == nil || cf == nil {
		t.Fatal("expected non-nil IssueTracker and CodeForge")
	}
	if any(it) != any(cf) {
		t.Errorf("expected the github tracker to reuse the code forge's execClient instance, got distinct values")
	}
}

func TestBuildForge_LocalTracker_ProbeCreatesIssuesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "issues")
	it, _ := buildForge("owner/repo", trackerSettings{issueTracker: "local", localIssuesDir: dir}, "ghp_faketoken", "")

	repo, err := it.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	want, _ := filepath.Abs(dir)
	if repo != want {
		t.Errorf("Probe() = %q, want %q", repo, want)
	}
	info, statErr := os.Stat(dir)
	if statErr != nil {
		t.Errorf("expected the local issues dir to exist after Probe, stat error: %v", statErr)
	} else if !info.IsDir() {
		t.Errorf("expected %q to be a directory", dir)
	}
}

func TestBuildForge_JiraTracker_ProbeHitsConfiguredBaseURLAndProjectKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/myself" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	it, _ := buildForge("owner/repo", trackerSettings{
		issueTracker:   "jira",
		jiraBaseURL:    srv.URL,
		jiraProjectKey: "ENG",
	}, "ghp_faketoken", "jira-faketoken")

	repo, err := it.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if repo != "ENG" {
		t.Errorf("Probe() = %q, want the configured project key %q", repo, "ENG")
	}
}
