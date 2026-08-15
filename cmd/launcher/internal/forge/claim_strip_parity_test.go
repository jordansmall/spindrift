package forge

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestClaimStripParity_AllWorkflows guards the same parity
// TestExecClient_TransitionState_ClaimRemoveLabelsMatchDispatchWorkflow
// (cmd/launcher/internal/forge/github/exec_test.go) pins for
// .github/workflows/agent-dispatch.yml alone, but broadened to all four
// claim-strip call sites across both forge mirrors: agent-dispatch.yml and
// agent-recover.yml under both .github/workflows and .forgejo/workflows
// (#2507). Each of those four workflow files hand-lists, in its "Claim the
// issue" step, the labels a claim removes; ClaimRemoveLabels below is the Go
// launcher's own idea of that same set. Nothing forces the two to agree
// except a human keeping four YAML files and one Go function in sync by
// hand, so this test reads all four files directly and fails loudly, naming
// the offending file and label, the moment they drift.
//
// The comparison is deliberately one-way: every label Go's
// ClaimRemoveLabels computes must appear in each workflow's claim-remove
// set, but not the reverse. agent-trigger and agent-recover are pure
// GitHub/Forgejo Actions trigger vocabulary — labels used to fire a dispatch
// or recovery run in the first place — with no forge.DispatchState
// equivalent, so the Go side can never produce them and never should. Set
// equality would therefore be the wrong assertion; subset is.
func TestClaimStripParity_AllWorkflows(t *testing.T) {
	labels := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	want := labels.ClaimRemoveLabels(Dispatchable, InProgress)

	// repoRoot is four levels up from this package directory
	// (forge -> internal -> launcher -> cmd -> repo root).
	repoRoot := filepath.Join("..", "..", "..", "..")

	cases := []struct {
		name string
		path string
		// key is the YAML key the workflow's "Claim the issue" step uses to
		// list the labels a claim removes. Both agent-dispatch.yml and
		// agent-recover.yml, in both forges, also have a *second*,
		// unrelated remove-label(s) occurrence later in the file (for
		// releasing agent-in-progress on completion). The claim step is
		// always the first step in the file that removes labels, so taking
		// the first regex match of this key is sufficient to land on the
		// claim step specifically — not just any remove-label(s) line. If a
		// future edit ever reorders a file so the completion step's
		// remove-label(s) line comes first, this extraction would silently
		// grab the wrong one; there is no cheap way to guard against that
		// beyond this comment, since both call sites share the same key
		// spelling in the forgejo files.
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
			raw, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatalf("read %s: %v", tc.path, err)
			}
			line := regexp.MustCompile(regexp.QuoteMeta(tc.key) + `:\s*(\S.*)`)
			m := line.FindStringSubmatch(string(raw))
			if m == nil {
				t.Fatalf("%s: no %q: line found", tc.path, tc.key)
			}
			workflowSet := map[string]bool{}
			for _, l := range strings.Fields(m[1]) {
				workflowSet[l] = true
			}

			for _, label := range want {
				if !workflowSet[label] {
					t.Errorf("%s: missing label %q (workflow claim-remove set: %q)", tc.path, label, m[1])
				}
			}
		})
	}
}
