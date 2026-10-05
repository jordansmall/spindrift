package main

import (
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
)

// relayCapabilityViolations builds each CODE_FORGE-valid row's read-only forge
// the way production does (newCodeForge, so local, which has no
// newReadOnlyCodeForge, is covered too) and reports every disagreement between
// the row's RelayCapable bit and the interfaces that forge implements.
func relayCapabilityViolations() []string {
	var out []string
	for _, row := range backendRows {
		if !row.ValidAsCodeForge {
			continue
		}
		c := minimalValidConfig()
		c.codeForge = row.Name
		c.boxForgeAndIssueAccess = "read-only"
		cf := newCodeForge(c, local.SanitizedParent{}, forge.NewFake())

		_, relay := cf.(forge.BundleRelay)
		_, draft := cf.(forge.DraftPRCreator)
		_, subjects := cf.(forge.BundleCommitSubjects)

		if relay != row.RelayCapable {
			out = append(out, fmt.Sprintf("%s: RelayCapable=%v but read-only forge %T implements BundleRelay=%v", row.Name, row.RelayCapable, cf, relay))
		}
		if draft != subjects {
			out = append(out, fmt.Sprintf("%s: read-only forge %T implements DraftPRCreator=%v but BundleCommitSubjects=%v; the PR pair must move together", row.Name, cf, draft, subjects))
		}
		if (draft || subjects) && !row.RelayCapable {
			out = append(out, fmt.Sprintf("%s: read-only forge %T implements a PR capability but RelayCapable=false", row.Name, cf))
		}
	}
	return out
}

// TestRelayCapableMatchesReadOnlyForgeInterfaces checks the registry's
// RelayCapable bit against what each row's live read-only forge implements:
// BundleRelay iff RelayCapable, DraftPRCreator iff BundleCommitSubjects, and
// no PR capability without RelayCapable.
// Read-only is the only scope that holds: since issue #4071 the read-write
// github and forgejo adapters implement DraftPRCreator without a bundle relay,
// so probing them would flag both rows. A nix-only row with no Go row is
// already caught by TestBackendRowsCoverRegistry plus nix/checks/schema-drift.nix's
// backend-registry-gen.
func TestRelayCapableMatchesReadOnlyForgeInterfaces(t *testing.T) {
	if v := relayCapabilityViolations(); len(v) > 0 {
		t.Errorf("RelayCapable disagrees with the read-only forge's interfaces:\n%s", strings.Join(v, "\n"))
	}
}

// bareForge hides every optional interface of the CodeForge it wraps.
type bareForge struct{ forge.CodeForge }

// draftOnlyForge adds DraftPRCreator but not BundleCommitSubjects.
type draftOnlyForge struct{ forge.CodeForge }

func (draftOnlyForge) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	return "", false, nil
}

// relayOnlyForge adds BundleRelay and nothing else.
type relayOnlyForge struct{ forge.CodeForge }

func (relayOnlyForge) RelayBundle(outboxDir, ref string) error { return nil }

// relayDraftForge adds BundleRelay and DraftPRCreator but not
// BundleCommitSubjects.
type relayDraftForge struct{ relayOnlyForge }

func (relayDraftForge) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	return "", false, nil
}

// prPairForge adds both PR capabilities but not BundleRelay.
type prPairForge struct{ draftOnlyForge }

func (prPairForge) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	return nil, nil
}

// TestRelayCapabilityViolations_ReportsLyingRows swaps one lying row per rule
// into backendRows, each fixture tripping only the rule it names, so a check
// loosened to one direction or dropped outright fails here.
func TestRelayCapabilityViolations_ReportsLyingRows(t *testing.T) {
	tests := []struct {
		name         string
		relayCapable bool
		newForge     func() forge.CodeForge
		want         string
	}{
		{"relay bit without BundleRelay", true, func() forge.CodeForge { return bareForge{forge.NewFake()} }, "RelayCapable=true but"},
		{"BundleRelay without relay bit", false, func() forge.CodeForge { return relayOnlyForge{forge.NewFake()} }, "RelayCapable=false but"},
		{"PR pair split", true, func() forge.CodeForge { return relayDraftForge{relayOnlyForge{forge.NewFake()}} }, "must move together"},
		{"PR capability without relay bit", false, func() forge.CodeForge { return prPairForge{draftOnlyForge{forge.NewFake()}} }, "implements a PR capability but RelayCapable=false"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := backendRows
			defer func() { backendRows = original }()
			row := fakeGitlabRow()
			row.RelayCapable = tc.relayCapable
			row.newCodeForge = func(config, local.SanitizedParent, forge.IssueTracker) forge.CodeForge { return tc.newForge() }
			backendRows = []backendRow{row}

			got := relayCapabilityViolations()
			if len(got) != 1 || !strings.Contains(got[0], tc.want) {
				t.Errorf("violations = %q, want exactly one containing %q", got, tc.want)
			}
		})
	}
}
