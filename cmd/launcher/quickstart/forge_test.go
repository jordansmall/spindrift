package main

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/credsource/credsourcetest"
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
	sh := backendkit.Shared{RepoSlug: "owner/repo", Labels: defaultDispatchLabels, Connectors: connectorruntime.FromRegistries(credsourcetest.Applied())}
	it, cf, err := buildForge(mustStrategy(t, "github"), mustStrategy(t, "github"), sh, knobLookup(nil, nil))
	if err != nil {
		t.Fatalf("buildForge: %v", err)
	}
	if it == nil || cf == nil {
		t.Fatal("expected non-nil IssueTracker and CodeForge")
	}
}

// The tracker comes from the tracker row and the code forge from the forge
// row, whichever backend each is.
func TestBuildForge_BuildsEachSeamFromItsOwnRow(t *testing.T) {
	var trackerBuilt, forgeBuilt []string
	stub := func(name string) backendkit.Strategy {
		return backendkit.Strategy{
			Descriptor: backend.Descriptor{Name: name},
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
	sh := backendkit.Shared{RepoSlug: "owner/repo", Labels: defaultDispatchLabels, Connectors: connectorruntime.FromRegistries(credsourcetest.Applied())}

	if _, _, err := buildForge(stub("a"), stub("b"), sh, knobLookup(nil, nil)); err != nil {
		t.Fatalf("buildForge: %v", err)
	}
	if got := strings.Join(trackerBuilt, ","); got != "a" {
		t.Errorf("trackers built = %q, want a", got)
	}
	if got := strings.Join(forgeBuilt, ","); got != "b" {
		t.Errorf("code forges built = %q, want b", got)
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
	if _, _, err := buildForge(s, s, backendkit.Shared{}, knobLookup(nil, nil)); !errors.Is(err, boom) {
		t.Errorf("buildForge error = %v, want it to wrap %v", err, boom)
	}
}
