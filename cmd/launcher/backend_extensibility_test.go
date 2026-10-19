package main

import (
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
)

// fakeGitlabRow stands in for a hypothetical future "gitlab" adapter package.
// It is never a real backend, just enough of a Strategy to exercise both the
// tracker and code-forge axes plus the token and doctor-hint machinery they
// carry.
func fakeGitlabRow() backendkit.Strategy {
	return backendkit.Strategy{
		Descriptor: backend.Descriptor{
			Name:             "gitlab",
			ValidAsTracker:   true,
			ValidAsCodeForge: true,
			TokenEnvVar:      "GITLAB_TOKEN",
			BoxTokenEnvVar:   "BOX_GITLAB_TOKEN",
			DoctorTokenHint:  "GITLAB_TOKEN",
			DoctorSlugHint:   "GITLAB_BASE_URL",
		},
		ValidateTracker: func(s backendkit.Shared, _ backendkit.Lookup) error {
			if s.RepoSlug == "" {
				return fmt.Errorf("set REPO_SLUG for ISSUE_TRACKER=gitlab")
			}
			return nil
		},
		NewIssueTracker: func(backendkit.Shared, backendkit.Lookup) (forge.IssueTracker, error) {
			return forge.NewFake(), nil
		},
		NewCodeForge: func(backendkit.Shared, backendkit.Lookup, forge.SanitizedParent, forge.IssueTracker) (forge.CodeForge, error) {
			return forge.NewFake(), nil
		},
	}
}

// withExtraStrategies registers extra beside the shipped Strategies for the
// test and rebuilds gateRegistry's token gates, which are built once at
// package init. Cleanup restores both.
func withExtraStrategies(t *testing.T, extra ...backendkit.Strategy) {
	t.Helper()
	restore := backendkit.ReplaceRegistryForTest(append(backendkit.Strategies(), extra...))
	rebuildTokenGates()
	t.Cleanup(func() {
		restore()
		rebuildTokenGates()
	})
}

// TestBackendRegistry_NewBackendNeedsOnlyRowAndNoOtherChanges pins issue #2267
// acceptance criterion #5: a new backend needs only a row plus an adapter
// package. It registers a fake Strategy and drives validate(),
// newIssueTracker(), newCodeForge(), boxTokenResolver(), and runDoctor()'s hint
// lookup, none of which knows the new backend exists.
func TestBackendRegistry_NewBackendNeedsOnlyRowAndNoOtherChanges(t *testing.T) {
	withExtraStrategies(t, fakeGitlabRow())

	row, ok := backendkit.ByName("gitlab")
	if !ok {
		t.Fatal("backendkit.ByName(\"gitlab\") ok = false, want true")
	}
	if row.Name != "gitlab" {
		t.Errorf("backendkit.ByName(\"gitlab\").name = %q, want %q", row.Name, "gitlab")
	}
	if !row.ValidAsTracker || !row.ValidAsCodeForge {
		t.Errorf("backendkit.ByName(\"gitlab\") validAsTracker/validAsCodeForge = %v/%v, want true/true", row.ValidAsTracker, row.ValidAsCodeForge)
	}

	c := minimalValidConfig()
	c.issueTracker = "gitlab"
	c.codeForge = "gitlab"
	if err := validate(c); err != nil {
		t.Errorf("validate() with ISSUE_TRACKER=CODE_FORGE=gitlab = %v, want nil", err)
	}

	it := newIssueTracker(c)
	if _, ok := it.(*forge.Fake); !ok {
		t.Fatalf("newIssueTracker(gitlab) returned %T, want *forge.Fake (the row's constructor)", it)
	}

	cf := newCodeForge(c, forge.SanitizedParent{}, it)
	if _, ok := cf.(*forge.Fake); !ok {
		t.Fatalf("newCodeForge(gitlab) returned %T, want *forge.Fake (the row's constructor)", cf)
	}

	t.Setenv("BOX_GITLAB_TOKEN", "box-gitlab-tok")
	resolved := boxTokenResolver(func(num, name string) string {
		return "unresolved-fallthrough"
	})("42", "GITLAB_TOKEN")
	if resolved != "box-gitlab-tok" {
		t.Errorf("boxTokenResolver resolved GITLAB_TOKEN = %q, want %q", resolved, "box-gitlab-tok")
	}

	// This backendkit.ByName call is the lookup runDoctor uses for its hints.
	hintRow, ok := backendkit.ByName(c.issueTracker)
	if !ok {
		t.Fatal("backendkit.ByName(c.issueTracker) ok = false, want true")
	}
	if hintRow.DoctorTokenHint != "GITLAB_TOKEN" {
		t.Errorf("doctorTokenHint = %q, want %q", hintRow.DoctorTokenHint, "GITLAB_TOKEN")
	}
	if hintRow.DoctorSlugHint != "GITLAB_BASE_URL" {
		t.Errorf("doctorSlugHint = %q, want %q", hintRow.DoctorSlugHint, "GITLAB_BASE_URL")
	}

	found := false
	for _, g := range gateRegistry {
		if g.Name == "read-only-token-gitlab" {
			found = true
		}
	}
	if !found {
		t.Error("gateRegistry has no read-only-token-gitlab gate for the fake Strategy")
	}
}

// TestValidateIssueTracker_InvalidMessageListsRuntimeRegisteredBackend pins issue
// #2520 slice 4: the ISSUE_TRACKER-invalid error's "must be ..." list comes from
// the registered Strategies filtered by ValidAsTracker, not a hand-typed
// literal in main.go. The test registers the fake Strategy at runtime, which a
// literal list could never see, so the message names "gitlab" only when the
// list comes from the registry.
func TestValidateIssueTracker_InvalidMessageListsRuntimeRegisteredBackend(t *testing.T) {
	withExtraStrategies(t, fakeGitlabRow())

	c := minimalValidConfig()
	c.issueTracker = "not-a-real-tracker"
	err := validate(c)
	if err == nil {
		t.Fatal("validate() should reject an unrecognised ISSUE_TRACKER")
	}
	if !strings.Contains(err.Error(), "gitlab") {
		t.Errorf("validate() error = %q, want it to mention the runtime-registered %q backend", err.Error(), "gitlab")
	}
}
