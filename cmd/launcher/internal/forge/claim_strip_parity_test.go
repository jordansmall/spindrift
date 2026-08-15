package forge_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// TestDispatchLabels_ClaimRemoveLabels_MatchesWorkflowFiles guards the same
// parity TestExecClient_TransitionState_ClaimRemoveLabelsMatchDispatchWorkflow
// (cmd/launcher/internal/forge/github/exec_test.go) pins for
// .github/workflows/agent-dispatch.yml alone, but broadened to all four
// claim-strip call sites across both forge mirrors: agent-dispatch.yml and
// agent-recover.yml under both .github/workflows and .forgejo/workflows
// (#2507). Each of those four workflow files hand-lists, in its claim step,
// the labels a claim removes; ClaimRemoveLabels below is the Go launcher's
// own idea of that same set. Nothing forces the two to agree except a human
// keeping four YAML files and one Go function in sync by hand, so this test
// reads all four files directly and fails loudly, naming the offending file
// and label, the moment they drift.
//
// The comparison is deliberately one-way: every label Go's
// ClaimRemoveLabels computes must appear in each workflow's claim-remove
// set, but not the reverse. agent-trigger and agent-recover are pure
// GitHub/Forgejo Actions trigger vocabulary — labels used to fire a dispatch
// or recovery run in the first place — with no forge.DispatchState
// equivalent, so the Go side can never produce them and never should. Set
// equality would therefore be the wrong assertion; subset is.
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

	// repoRoot is four levels up from this package directory
	// (forge -> internal -> launcher -> cmd -> repo root).
	repoRoot := filepath.Join("..", "..", "..", "..")

	cases := []struct {
		name string
		path string
		// key is the YAML key the workflow's claim step uses to list the
		// labels a claim removes — a literal "Claim the issue" step in both
		// .forgejo files; folded into the .github "Agent setup" step
		// instead (see agent-dispatch.yml), which sets claim-remove-labels
		// alongside the launcher's other flags. Both agent-dispatch.yml and
		// agent-recover.yml, in both forges, also have a *second*,
		// unrelated remove-label(s) occurrence later in the file (for
		// releasing agent-in-progress on completion). The claim step is
		// always the first step in the file that removes labels, so taking
		// the first regex match of this key is sufficient to land on the
		// claim step specifically — not just any remove-label(s) line.
		//
		// Reorder risk is forge-specific. The .github key
		// (claim-remove-labels) has no relation to the completion step's
		// --remove-label CLI flag, so no .github reorder can make this
		// regex match the wrong step. The two .forgejo files do share one
		// key spelling (remove-labels) across both steps, so a reorder
		// there could make this regex land on the completion step's list
		// instead — but that failure is loud, not silent: the completion
		// step's set (just agent-in-progress) is not a superset of want, so
		// the subset check below errors rather than false-passing.
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
