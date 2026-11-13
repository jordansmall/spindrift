package main

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
)

// withRelayIncapableGit swaps in a registry whose git row lacks the
// relayCapable bit. Every registered forge is relay-capable since issue #6898,
// so the gate's rejection path needs a synthetic row to stay under test.
func withRelayIncapableGit(t *testing.T) {
	t.Helper()
	rows := backendkit.Strategies()
	for i := range rows {
		if rows[i].Name == "git" {
			rows[i].RelayCapable = false
		}
	}
	t.Cleanup(backendkit.ReplaceRegistryForTest(rows))
}

// A relay-incapable forge with jira would fail under read-only on the forge
// axis, so this fixture pins that read-write is a complete no-op rather than a
// gate that happens to pass.
func TestReadOnlyCapabilityGate_ReadWriteIsNoOp(t *testing.T) {
	withRelayIncapableGit(t)
	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = "read-write"
	c.CodeForge = "git"
	c.IssueTracker = "jira"
	if err := checkReadOnlyCapabilityGate(c); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() with read-write = %v, want nil", err)
	}
}

// mkHarness's readOnlyCapabilityOk eval assert (issue #2526 slice 2) already
// proves every combination a Consumer can bake into an image, so the Go gate
// (issue #2526 slice 3) has shrunk to a registry lookup by name that backstops a
// runtime override of these three knobs. That is why these cases need no live
// cf/it fixtures.
func TestReadOnlyCapabilityGate_Table(t *testing.T) {
	cases := []struct {
		name           string
		access         string
		codeForge      string
		issueTracker   string
		wantErr        bool
		wantSubstrs    []string
		wantNotSubstrs []string
		// relayIncapableGit runs the case against a registry whose git row lacks
		// RelayCapable (withRelayIncapableGit).
		relayIncapableGit bool
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
			name:         "read-only git/github passes",
			access:       "read-only",
			codeForge:    "git",
			issueTracker: "github",
			wantErr:      false,
		},
		{
			name:         "read-only git/jira passes",
			access:       "read-only",
			codeForge:    "git",
			issueTracker: "jira",
			wantErr:      false,
		},
		{
			// A forge without the relayCapable bit fails whatever tracker pairs
			// with it; the real git row is relay-capable (issue #6898).
			name:              "read-only relay-incapable forge fails naming CODE_FORGE and bundle-relay",
			relayIncapableGit: true,
			access:            "read-only",
			codeForge:         "git",
			issueTracker:      "github",
			wantErr:           true,
			wantSubstrs: []string{
				"BOX_FORGE_AND_ISSUE_ACCESS",
				"does not implement",
				"bundle-relay",
				`the selected CODE_FORGE="git"`,
			},
		},
		{
			// Every valid tracker can post from the host, so this reaches the
			// tracker axis only through the forge-only git row, which Validate()
			// rejects as a tracker earlier: the gate is the backstop here.
			name:         "read-only github/git tracker fails naming ISSUE_TRACKER and issue-filing",
			access:       "read-only",
			codeForge:    "github",
			issueTracker: "git",
			wantErr:      true,
			wantSubstrs: []string{
				"BOX_FORGE_AND_ISSUE_ACCESS",
				"does not implement",
				"issue-filing",
				`the selected ISSUE_TRACKER="git"`,
			},
		},
		{
			name:         "read-only github/jira passes",
			access:       "read-only",
			codeForge:    "github",
			issueTracker: "jira",
			wantErr:      false,
		},
		{
			name:         "read-only forgejo/jira passes",
			access:       "read-only",
			codeForge:    "forgejo",
			issueTracker: "jira",
			wantErr:      false,
		},
		{
			// jira can post from the host, so a relay-incapable forge/jira pair fails on the
			// forge axis alone, with the forge message.
			name:              "read-only relay-incapable forge/jira fails on the forge axis first",
			relayIncapableGit: true,
			access:            "read-only",
			codeForge:         "git",
			issueTracker:      "jira",
			wantErr:           true,
			wantSubstrs: []string{
				"bundle-relay",
				`the selected CODE_FORGE="git"`,
			},
		},
		{
			// Validate() rejects an unregistered name earlier, so this gate
			// is only the backstop, and it must report a lookup miss rather
			// than "does not implement bundle-relay", a claim that presumes
			// a registered row to check a bit on.
			name:         "read-only unregistered CODE_FORGE fails naming it unregistered, not capability-incapable",
			access:       "read-only",
			codeForge:    "bogus-forge",
			issueTracker: "github",
			wantErr:      true,
			wantSubstrs: []string{
				"BOX_FORGE_AND_ISSUE_ACCESS",
				`CODE_FORGE="bogus-forge"`,
				"not a registered",
			},
			wantNotSubstrs: []string{
				"does not implement",
				"bundle-relay",
			},
		},
		{
			name:         "read-only unregistered ISSUE_TRACKER fails naming it unregistered, not capability-incapable",
			access:       "read-only",
			codeForge:    "github",
			issueTracker: "bogus-tracker",
			wantErr:      true,
			wantSubstrs: []string{
				"BOX_FORGE_AND_ISSUE_ACCESS",
				`ISSUE_TRACKER="bogus-tracker"`,
				"not a registered",
			},
			wantNotSubstrs: []string{
				"does not implement",
				"issue-filing",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.relayIncapableGit {
				withRelayIncapableGit(t)
			}
			c := minimalValidConfig()
			c.BoxForgeAndIssueAccess = tc.access
			c.CodeForge = tc.codeForge
			c.IssueTracker = tc.issueTracker

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
				for _, s := range tc.wantNotSubstrs {
					if strings.Contains(err.Error(), s) {
						t.Errorf("error %q should not contain %q", err.Error(), s)
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

// Issue #2942's AC5 keeps this gate's wording byte-identical to the four older
// gates: wrapping with fmt.Errorf("%w: ...", errLaunchGateConfigInvalid, ...)
// would prepend the sentinel's own text to the message dispatch, recover and
// preview print verbatim to stderr. errors.Is must still hold so doctor.go's
// doctorExitCodeFor keeps classifying the exit code.
func TestReadOnlyCapabilityGate_ErrorTextHasNoSentinelPrefix(t *testing.T) {
	withRelayIncapableGit(t)
	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = "read-only"
	c.CodeForge = "git"
	c.IssueTracker = "github"

	err := checkReadOnlyCapabilityGate(c)
	if err == nil {
		t.Fatal("checkReadOnlyCapabilityGate() = nil, want an error")
	}
	if strings.Contains(err.Error(), "launch gate config invalid") {
		t.Errorf("error %q should not contain the sentinel's own text %q", err.Error(), "launch gate config invalid")
	}
	if !strings.HasPrefix(err.Error(), "BOX_FORGE_AND_ISSUE_ACCESS=read-only") {
		t.Errorf("error %q should start with %q", err.Error(), "BOX_FORGE_AND_ISSUE_ACCESS=read-only")
	}
	if !errors.Is(err, errLaunchGateConfigInvalid) {
		t.Errorf("errors.Is(err, errLaunchGateConfigInvalid) = false, want true (doctor.go's exit-code classification depends on this)")
	}
}
