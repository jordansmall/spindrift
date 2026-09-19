package forge_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// Four workflow files (agent-dispatch.yml and agent-recover.yml under both
// .github and .forgejo) hand-list the labels a claim removes, and only a
// human keeps them in sync with ClaimRemoveLabels (#2507). The check is a
// subset, not set equality: agent-trigger and agent-recover are Actions
// trigger labels with no forge.DispatchState equivalent, so Go never emits them.
func TestDispatchLabels_ClaimRemoveLabels_MatchesWorkflowFiles(t *testing.T) {
	labels := forge.DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	want := labels.ClaimRemoveLabels(forge.Dispatchable, forge.InProgress)
	if len(want) == 0 {
		t.Fatal("ClaimRemoveLabels(Dispatchable, InProgress) returned no labels — parity check would pass vacuously")
	}

	repoRoot := filepath.Join("..", "..", "..", "..")

	cases := []struct {
		name string
		path string
		// The YAML key whose first match is the claim step's remove-label
		// list. Every one of these files also removes labels later, when it
		// releases agent-in-progress on completion; the claim step comes
		// first, so the first match lands on it. A .forgejo reorder could
		// match the completion step, but that set fails the subset check.
		key string
	}{
		{
			name: "github dispatch",
			path: filepath.Join(repoRoot, ".github", "workflows", "agent-dispatch.yml"),
			key:  "claim-remove-labels",
		},
		{
			name: "github recover",
			path: filepath.Join(repoRoot, ".github", "workflows", "agent-recover.yml"),
			key:  "claim-remove-labels",
		},
		{
			name: "forgejo dispatch",
			path: filepath.Join(repoRoot, ".forgejo", "workflows", "agent-dispatch.yml"),
			key:  "remove-labels",
		},
		{
			name: "forgejo recover",
			path: filepath.Join(repoRoot, ".forgejo", "workflows", "agent-recover.yml"),
			key:  "remove-labels",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workflowSet, rawValue := forgetest.ParseWorkflowRemoveLabelSet(t, tc.path, tc.key)

			for _, label := range want {
				if !workflowSet[label] {
					t.Errorf("%s: missing label %q (workflow claim-remove set: %q)", tc.path, label, rawValue)
				}
			}
		})
	}
}
