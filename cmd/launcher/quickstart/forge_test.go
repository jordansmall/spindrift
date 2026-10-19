package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
)

func mustStrategy(t *testing.T, name string) backendkit.Strategy {
	t.Helper()
	s, ok := backendkit.ByName(name)
	if !ok {
		t.Fatalf("backend %q not registered", name)
	}
	return s
}

func TestBuildForge_GithubTracker_BuildsBothSeams(t *testing.T) {
	sh := backendkit.Shared{RepoSlug: "owner/repo", Labels: defaultDispatchLabels, BranchPrefix: defaultBranchPrefix}
	it, cf, err := buildForge(mustStrategy(t, "github"), sh, knobLookup(nil, "", ""))
	if err != nil {
		t.Fatalf("buildForge: %v", err)
	}
	if it == nil || cf == nil {
		t.Fatal("expected non-nil IssueTracker and CodeForge")
	}
}

func TestBuildForge_ForgejoTracker_ProbeHitsConfiguredBaseURLWithToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"full_name":"owner/repo"}`))
	}))
	defer srv.Close()

	s := mustStrategy(t, "forgejo")
	l := knobLookup(map[string]string{"FORGEJO_BASE_URL": srv.URL}, s.TokenEnvVar, "forgejo-faketoken")
	it, cf, err := buildForge(s, backendkit.Shared{RepoSlug: "owner/repo", Labels: defaultDispatchLabels}, l)
	if err != nil {
		t.Fatalf("buildForge: %v", err)
	}
	if cf == nil {
		t.Fatal("expected a non-nil CodeForge")
	}

	repo, err := it.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if repo != "owner/repo" {
		t.Errorf("Probe() = %q, want %q", repo, "owner/repo")
	}
	if !strings.Contains(gotAuth, "forgejo-faketoken") {
		t.Errorf("Authorization = %q, want it to carry the token passed through the Lookup", gotAuth)
	}
}

// A row without the forge axis borrows the default code forge; a row that
// declares the axis supplies its own.
func TestBuildForge_CodeForgeStrategy_FollowsForgeAxis(t *testing.T) {
	var trackerBuilt, forgeBuilt []string
	stub := func(name string, axes ...string) backendkit.Strategy {
		return backendkit.Strategy{
			Descriptor: backend.Descriptor{Name: name, Quickstart: &backend.QuickstartBlock{Axes: axes}},
			NewIssueTracker: func(backendkit.Shared, backendkit.Lookup) (forge.IssueTracker, error) {
				trackerBuilt = append(trackerBuilt, name)
				return forge.NewFake(), nil
			},
			NewCodeForge: func(backendkit.Shared, backendkit.Lookup, forge.SanitizedParent, forge.IssueTracker) (forge.CodeForge, error) {
				forgeBuilt = append(forgeBuilt, name)
				return forge.NewFake(), nil
			},
		}
	}
	sh := backendkit.Shared{RepoSlug: "owner/repo", Labels: defaultDispatchLabels, BranchPrefix: defaultBranchPrefix}

	if _, _, err := buildForge(stub("tracker-only", axisTracker), sh, knobLookup(nil, "", "")); err != nil {
		t.Fatalf("buildForge: %v", err)
	}
	if _, _, err := buildForge(stub("both", axisTracker, axisForge), sh, knobLookup(nil, "", "")); err != nil {
		t.Fatalf("buildForge: %v", err)
	}
	if got := strings.Join(trackerBuilt, ","); got != "tracker-only,both" {
		t.Errorf("trackers built = %q, want tracker-only,both", got)
	}
	if got := strings.Join(forgeBuilt, ","); got != "both" {
		t.Errorf("stub code forges built = %q, want only both (tracker-only borrows github's)", got)
	}
}

func TestBuildForge_ConstructorError_IsReturnedNotPanicked(t *testing.T) {
	boom := errors.New("bad knob")
	s := backendkit.Strategy{
		Descriptor: backend.Descriptor{Name: "boom", Quickstart: &backend.QuickstartBlock{Axes: []string{axisTracker, axisForge}}},
		NewIssueTracker: func(backendkit.Shared, backendkit.Lookup) (forge.IssueTracker, error) {
			return nil, boom
		},
	}
	if _, _, err := buildForge(s, backendkit.Shared{}, knobLookup(nil, "", "")); !errors.Is(err, boom) {
		t.Errorf("buildForge error = %v, want it to wrap %v", err, boom)
	}
}
