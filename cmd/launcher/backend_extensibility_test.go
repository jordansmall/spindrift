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
			DoctorTokenHint:  "GITLAB_TOKEN",
			DoctorSlugHint:   "GITLAB_BASE_URL",
		},
		ValidateTracker: func(s backendkit.Shared, _ backendkit.Lookup) error {
			if s.RepoSlug == "" {
				return fmt.Errorf("pass --repo-slug owner/repo or set forge.repoSlug when ISSUE_TRACKER=gitlab")
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
// test; cleanup restores the registry.
func withExtraStrategies(t *testing.T, extra ...backendkit.Strategy) {
	t.Helper()
	t.Cleanup(backendkit.ReplaceRegistryForTest(append(backendkit.Strategies(), extra...)))
}

// TestBackendRegistry_NewBackendNeedsOnlyRowAndNoOtherChanges pins issue #2267
// acceptance criterion #5: a new backend needs only a row plus an adapter
// package. It registers a fake Strategy and drives validate(),
// newIssueTracker(), newCodeForge(), and runDoctor()'s hint
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
	c.IssueTracker = "gitlab"
	c.CodeForge = "gitlab"
	c.setRowKnob("GITLAB_TOKEN", "tok")
	if err := validate(c); err != nil {
		t.Errorf("validate() with ISSUE_TRACKER=CODE_FORGE=gitlab = %v, want nil", err)
	}

	it := mustBuild(newIssueTracker(c))
	if _, ok := it.(*forge.Fake); !ok {
		t.Fatalf("newIssueTracker(gitlab) returned %T, want *forge.Fake (the row's constructor)", it)
	}

	cf := mustNewCodeForge(t, c, forge.SanitizedParent{}, it)
	if _, ok := cf.(*forge.Fake); !ok {
		t.Fatalf("newCodeForge(gitlab) returned %T, want *forge.Fake (the row's constructor)", cf)
	}

	// This backendkit.ByName call is the lookup runDoctor uses for its hints.
	hintRow, ok := backendkit.ByName(c.IssueTracker)
	if !ok {
		t.Fatal("backendkit.ByName(c.issueTracker) ok = false, want true")
	}
	if hintRow.DoctorTokenHint != "GITLAB_TOKEN" {
		t.Errorf("doctorTokenHint = %q, want %q", hintRow.DoctorTokenHint, "GITLAB_TOKEN")
	}
	if hintRow.DoctorSlugHint != "GITLAB_BASE_URL" {
		t.Errorf("doctorSlugHint = %q, want %q", hintRow.DoctorSlugHint, "GITLAB_BASE_URL")
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
	c.IssueTracker = "not-a-real-tracker"
	err := validate(c)
	if err == nil {
		t.Fatal("validate() should reject an unrecognised ISSUE_TRACKER")
	}
	if !strings.Contains(err.Error(), "gitlab") {
		t.Errorf("validate() error = %q, want it to mention the runtime-registered %q backend", err.Error(), "gitlab")
	}
}
