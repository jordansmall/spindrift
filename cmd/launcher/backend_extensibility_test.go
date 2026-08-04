package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
)

// fakeGitlabRow constructs a minimal stand-in backendRow for a hypothetical
// future "gitlab" adapter package — never a real backend, just enough of a
// row to exercise both the tracker and code-forge axes plus the token/
// doctor-hint/read-only-gate machinery those axes carry.
func fakeGitlabRow() backendRow {
	return backendRow{
		name:             "gitlab",
		validAsTracker:   true,
		validAsCodeForge: true,
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
		tokenEnvVar:    "GITLAB_TOKEN",
		boxTokenEnvVar: "BOX_GITLAB_TOKEN",

		doctorTokenHint: "GITLAB_TOKEN",
		doctorSlugHint:  "GITLAB_BASE_URL",

		readOnlyTokenGate: func(c config, w io.Writer) (bool, error) {
			return true, nil
		},
		readOnlyGateOkMessage: func(bool) string {
			return "ok: read-only token gate satisfied — BOX_GITLAB_TOKEN is set and distinct"
		},
	}
}

// TestBackendRegistry_NewBackendNeedsOnlyRowAndNoOtherChanges pins issue
// #2267's acceptance criterion #5: registering a new backend requires only a
// row plus an adapter package. It appends a fake "gitlab" backendRow
// (fakeGitlabRow, above — standing in for what a real future adapter
// package's row would look like) directly to the package-level backendRows
// registry, with no change whatsoever to validate(), newIssueTracker(),
// newCodeForge(), boxTokenResolver(), reportReadOnlyTokenGates(), or
// runDoctor()'s doctor-hint lookup — then drives every one of those
// dispatch sites with ISSUE_TRACKER=gitlab / CODE_FORGE=gitlab and checks
// each one routes to the new row instead of falling back to github's
// default or failing validation. Every assertion below was red-confirmed by
// temporarily commenting out the backendRows = append(...) line below (and
// swapping the two t.Fatal[f] calls that would otherwise short-circuit
// later assertions to t.Error[f]) and re-running: each assertion failed on
// its own, proving none is vacuously true.
func TestBackendRegistry_NewBackendNeedsOnlyRowAndNoOtherChanges(t *testing.T) {
	original := backendRows
	backendRows = append(append([]backendRow{}, original...), fakeGitlabRow())
	defer func() { backendRows = original }()

	// backendByName finds the new row by name.
	row, ok := backendByName("gitlab")
	if !ok {
		t.Fatal("backendByName(\"gitlab\") ok = false, want true")
	}
	if row.name != "gitlab" {
		t.Errorf("backendByName(\"gitlab\").name = %q, want %q", row.name, "gitlab")
	}
	if !row.validAsTracker || !row.validAsCodeForge {
		t.Errorf("backendByName(\"gitlab\") validAsTracker/validAsCodeForge = %v/%v, want true/true", row.validAsTracker, row.validAsCodeForge)
	}

	// validate() accepts ISSUE_TRACKER=gitlab / CODE_FORGE=gitlab with no
	// axis-validity edit and runs the row's own validateTracker.
	c := minimalValidConfig()
	c.issueTracker = "gitlab"
	c.codeForge = "gitlab"
	if err := validate(c); err != nil {
		t.Errorf("validate() with ISSUE_TRACKER=CODE_FORGE=gitlab = %v, want nil", err)
	}

	// newIssueTracker dispatches to the row's constructor, not the github
	// fallback.
	it := newIssueTracker(c)
	if _, ok := it.(*forge.Fake); !ok {
		t.Fatalf("newIssueTracker(gitlab) returned %T, want *forge.Fake (the row's constructor)", it)
	}

	// newCodeForge dispatches to the row's constructor, not the github
	// fallback.
	cf := newCodeForge(c, local.SanitizedParent{}, it)
	if _, ok := cf.(*forge.Fake); !ok {
		t.Fatalf("newCodeForge(gitlab) returned %T, want *forge.Fake (the row's constructor)", cf)
	}

	// boxTokenResolver's registry walk honors the new row's
	// tokenEnvVar/boxTokenEnvVar pair.
	t.Setenv("BOX_GITLAB_TOKEN", "box-gitlab-tok")
	resolved := boxTokenResolver(func(num, name string) string {
		return "unresolved-fallthrough"
	})("42", "GITLAB_TOKEN")
	if resolved != "box-gitlab-tok" {
		t.Errorf("boxTokenResolver resolved GITLAB_TOKEN = %q, want %q", resolved, "box-gitlab-tok")
	}

	// reportReadOnlyTokenGates dispatches to the new row's gate and prints
	// its ok message.
	c.boxForgeAndIssueAccess = "read-only"
	var buf bytes.Buffer
	if err := reportReadOnlyTokenGates(c, &buf); err != nil {
		t.Fatalf("reportReadOnlyTokenGates() error = %v, want nil", err)
	}
	if !strings.Contains(buf.String(), "BOX_GITLAB_TOKEN is set and distinct") {
		t.Errorf("reportReadOnlyTokenGates output = %q, want it to contain the gitlab row's readOnlyGateOkMessage text", buf.String())
	}

	// runDoctor's hint lookup (backendByName(c.issueTracker)) resolves the
	// new row's doctor hints.
	hintRow, ok := backendByName(c.issueTracker)
	if !ok {
		t.Fatal("backendByName(c.issueTracker) ok = false, want true")
	}
	if hintRow.doctorTokenHint != "GITLAB_TOKEN" {
		t.Errorf("doctorTokenHint = %q, want %q", hintRow.doctorTokenHint, "GITLAB_TOKEN")
	}
	if hintRow.doctorSlugHint != "GITLAB_BASE_URL" {
		t.Errorf("doctorSlugHint = %q, want %q", hintRow.doctorSlugHint, "GITLAB_BASE_URL")
	}
}
