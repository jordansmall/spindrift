package main

import (
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
)

// fakeGitlabRow stands in for a hypothetical future "gitlab" adapter package.
// It is never a real backend, just enough of a row to exercise both the tracker
// and code-forge axes plus the token and doctor-hint machinery they carry.
func fakeGitlabRow() backendRow {
	return backendRow{
		Descriptor: backend.Descriptor{
			Name:             "gitlab",
			ValidAsTracker:   true,
			ValidAsCodeForge: true,
			TokenEnvVar:      "GITLAB_TOKEN",
			DoctorTokenHint:  "GITLAB_TOKEN",
			DoctorSlugHint:   "GITLAB_BASE_URL",
		},
		validateTracker: func(c config) error {
			if c.repoSlug == "" {
				return fmt.Errorf("set REPO_SLUG for ISSUE_TRACKER=gitlab")
			}
			return nil
		},
		newIssueTracker: func(c config) forge.IssueTracker {
			return forge.NewFake()
		},
		newCodeForge: func(c config, _ local.SanitizedParent, _ forge.IssueTracker) forge.CodeForge {
			return forge.NewFake()
		},
		boxTokenEnvVar: "BOX_GITLAB_TOKEN",
	}
}

// TestBackendRegistry_NewBackendNeedsOnlyRowAndNoOtherChanges pins issue #2267
// acceptance criterion #5: a new backend needs only a row plus an adapter
// package. It appends a fake row to backendRows and drives validate(),
// newIssueTracker(), newCodeForge(), boxTokenResolver(), and runDoctor()'s hint
// lookup, none of which knows the new backend exists.
func TestBackendRegistry_NewBackendNeedsOnlyRowAndNoOtherChanges(t *testing.T) {
	original := backendRows
	backendRows = append(append([]backendRow{}, original...), fakeGitlabRow())
	defer func() { backendRows = original }()

	row, ok := backendByName("gitlab")
	if !ok {
		t.Fatal("backendByName(\"gitlab\") ok = false, want true")
	}
	if row.Name != "gitlab" {
		t.Errorf("backendByName(\"gitlab\").name = %q, want %q", row.Name, "gitlab")
	}
	if !row.ValidAsTracker || !row.ValidAsCodeForge {
		t.Errorf("backendByName(\"gitlab\") validAsTracker/validAsCodeForge = %v/%v, want true/true", row.ValidAsTracker, row.ValidAsCodeForge)
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

	cf := newCodeForge(c, local.SanitizedParent{}, it)
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

	// This backendByName call is the lookup runDoctor uses for its hints.
	hintRow, ok := backendByName(c.issueTracker)
	if !ok {
		t.Fatal("backendByName(c.issueTracker) ok = false, want true")
	}
	if hintRow.DoctorTokenHint != "GITLAB_TOKEN" {
		t.Errorf("doctorTokenHint = %q, want %q", hintRow.DoctorTokenHint, "GITLAB_TOKEN")
	}
	if hintRow.DoctorSlugHint != "GITLAB_BASE_URL" {
		t.Errorf("doctorSlugHint = %q, want %q", hintRow.DoctorSlugHint, "GITLAB_BASE_URL")
	}

	// Do not re-add a read-only-token-gate assertion here. Issue #2942 retired
	// it: gateRegistry holds fixed github/forgejo entries, so a third backend's
	// gate was only ever reported, never enforced at dispatch.
}

// TestValidateIssueTracker_InvalidMessageListsRuntimeRegisteredBackend pins issue
// #2520 slice 4: the ISSUE_TRACKER-invalid error's "must be ..." list comes from
// backendRows filtered by ValidAsTracker, not a hand-typed literal in main.go.
// The test appends the fake row at runtime, which a literal list could never
// see, so the message names "gitlab" only when the list comes from backendRows.
func TestValidateIssueTracker_InvalidMessageListsRuntimeRegisteredBackend(t *testing.T) {
	original := backendRows
	backendRows = append(append([]backendRow{}, original...), fakeGitlabRow())
	defer func() { backendRows = original }()

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
