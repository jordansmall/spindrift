package main

import (
	"strings"
	"testing"
)

// TestReadOnlyCapabilityGate_ReadWriteIsNoOp verifies that
// checkReadOnlyCapabilityGate never rejects a backend combination when
// BOX_FORGE_AND_ISSUE_ACCESS is read-write (the default) — read-write must
// stay a complete no-op regardless of which forge/tracker names are
// selected, even a combination that would fail under read-only (git/jira).
func TestReadOnlyCapabilityGate_ReadWriteIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-write"
	c.codeForge = "git"
	c.issueTracker = "jira"
	if err := checkReadOnlyCapabilityGate(c); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() with read-write = %v, want nil", err)
	}
}

// TestReadOnlyCapabilityGate_Table exercises checkReadOnlyCapabilityGate
// (issue #2526 slice 3) purely by (BOX_FORGE_AND_ISSUE_ACCESS, CODE_FORGE,
// ISSUE_TRACKER) name, now that mkHarness's readOnlyCapabilityOk eval assert
// (issue #2526 slice 2) already proves every combination a Consumer can bake
// into an image coherent at `nix build` time — the Go gate has shrunk to a
// registry lookup by name, a backstop for a runtime override of these three
// knobs past what nix validated, so it no longer needs live cf/it fixtures
// at all.
func TestReadOnlyCapabilityGate_Table(t *testing.T) {
	cases := []struct {
		name         string
		access       string
		codeForge    string
		issueTracker string
		wantErr      bool
		wantSubstrs  []string
	}{
		{
			name:         "read-only github/github passes",
			access:       "read-only",
			codeForge:    "github",
			issueTracker: "github",
			wantErr:      false,
		},
		{
			name:         "read-only local/local passes",
			access:       "read-only",
			codeForge:    "local",
			issueTracker: "local",
			wantErr:      false,
		},
		{
			name:         "read-only forgejo/forgejo passes",
			access:       "read-only",
			codeForge:    "forgejo",
			issueTracker: "forgejo",
			wantErr:      false,
		},
		{
			// git is relay-incapable on the forge axis (no host-mediation
			// seam at all) — fails regardless of which tracker pairs with
			// it, and the error must name CODE_FORGE=git and bundle-relay,
			// not the tracker.
			name:         "read-only git forge fails naming CODE_FORGE and bundle-relay",
			access:       "read-only",
			codeForge:    "git",
			issueTracker: "github",
			wantErr:      true,
			wantSubstrs: []string{
				"BOX_FORGE_AND_ISSUE_ACCESS",
				"does not implement",
				"bundle-relay",
				`the selected CODE_FORGE="git"`,
			},
		},
		{
			// jira is host-posting-incapable on the tracker axis — fails
			// even though github (the forge) is fully capable, and the
			// error must name ISSUE_TRACKER=jira and issue-filing.
			name:         "read-only github/jira fails naming ISSUE_TRACKER and issue-filing",
			access:       "read-only",
			codeForge:    "github",
			issueTracker: "jira",
			wantErr:      true,
			wantSubstrs: []string{
				"BOX_FORGE_AND_ISSUE_ACCESS",
				"does not implement",
				"issue-filing",
				`the selected ISSUE_TRACKER="jira"`,
			},
		},
		{
			// Both axes incapable: the forge axis (checked first, matching
			// the gate's own check order) must win the error message.
			name:         "read-only git/jira fails on the forge axis first",
			access:       "read-only",
			codeForge:    "git",
			issueTracker: "jira",
			wantErr:      true,
			wantSubstrs: []string{
				"bundle-relay",
				`the selected CODE_FORGE="git"`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalValidConfig()
			c.boxForgeAndIssueAccess = tc.access
			c.codeForge = tc.codeForge
			c.issueTracker = tc.issueTracker

			err := checkReadOnlyCapabilityGate(c)
			if tc.wantErr {
				if err == nil {
					t.Fatal("checkReadOnlyCapabilityGate() = nil, want an error naming the missing seam")
				}
				for _, s := range tc.wantSubstrs {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q should contain %q", err.Error(), s)
					}
				}
				return
			}
			if err != nil {
				t.Errorf("checkReadOnlyCapabilityGate() = %v, want nil", err)
			}
		})
	}
}
