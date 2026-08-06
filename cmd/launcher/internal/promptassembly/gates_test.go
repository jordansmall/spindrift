package promptassembly

import "testing"

// TestGatesSkillsBaking covers the CAVEMAN_BAKED/TDD_BAKED/COMMIT_BAKED/
// CODE_REVIEW_BAKED gates (entrypoint.sh phase_prompt_assembly, lines
// 727-747): each fires only when the corresponding skill was actually baked
// at DRIVER_SKILLS_DIR/<name>/SKILL.md — a per-skill presence flag the CLI
// boundary resolves via a filesystem stat before ever reaching this pure
// Env, so Gates itself only branches on the already-resolved bool.
func TestGatesSkillsBaking(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want map[string]bool
	}{
		{
			name: "no skill baked",
			env:  Env{},
			want: map[string]bool{
				"CAVEMAN_BAKED":     false,
				"TDD_BAKED":         false,
				"COMMIT_BAKED":      false,
				"CODE_REVIEW_BAKED": false,
			},
		},
		{
			name: "every skill baked",
			env: Env{
				CavemanSkillBaked:    true,
				TDDSkillBaked:        true,
				CommitSkillBaked:     true,
				CodeReviewSkillBaked: true,
			},
			want: map[string]bool{
				"CAVEMAN_BAKED":     true,
				"TDD_BAKED":         true,
				"COMMIT_BAKED":      true,
				"CODE_REVIEW_BAKED": true,
			},
		},
		{
			name: "only caveman baked",
			env: Env{
				CavemanSkillBaked: true,
			},
			want: map[string]bool{
				"CAVEMAN_BAKED":     true,
				"TDD_BAKED":         false,
				"COMMIT_BAKED":      false,
				"CODE_REVIEW_BAKED": false,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(tc.env)
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(%+v)[%q] = %v, want %v", tc.env, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesOrchestratorReviewLoop covers ORCHESTRATOR (entrypoint.sh:
// 761-762), the REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR exactly-one-on
// pairing it drives (entrypoint.sh: 771-779), and FILER_ENABLED/
// WORKER_PROVISIONED (entrypoint.sh: 781-799), each derived from whether
// AgentsJSONTemplate carries a "filer"/"worker" key — orthogonal to
// ORCHESTRATOR, so every combination of the two axes is exercised.
func TestGatesOrchestratorReviewLoop(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want map[string]bool
	}{
		{
			name: "orchestrator off, no agents template",
			env:  Env{},
			want: map[string]bool{
				"ORCHESTRATOR":             false,
				"REVIEW_LOOP_INLINE":       true,
				"REVIEW_LOOP_ORCHESTRATOR": false,
				"FILER_ENABLED":            false,
				"WORKER_PROVISIONED":       false,
			},
		},
		{
			name: "orchestrator on, no agents template",
			env:  Env{OrchestratorEnabled: true},
			want: map[string]bool{
				"ORCHESTRATOR":             true,
				"REVIEW_LOOP_INLINE":       false,
				"REVIEW_LOOP_ORCHESTRATOR": true,
				"FILER_ENABLED":            false,
				"WORKER_PROVISIONED":       false,
			},
		},
		{
			name: "orchestrator off, filer and worker both provisioned",
			env: Env{
				AgentsJSONTemplate: `{"filer":{"model":"m"},"worker":{"model":"m"}}`,
			},
			want: map[string]bool{
				"ORCHESTRATOR":             false,
				"REVIEW_LOOP_INLINE":       true,
				"REVIEW_LOOP_ORCHESTRATOR": false,
				"FILER_ENABLED":            true,
				"WORKER_PROVISIONED":       true,
			},
		},
		{
			name: "orchestrator on, only filer provisioned",
			env: Env{
				OrchestratorEnabled: true,
				AgentsJSONTemplate:  `{"filer":{"model":"m"}}`,
			},
			want: map[string]bool{
				"ORCHESTRATOR":             true,
				"REVIEW_LOOP_INLINE":       false,
				"REVIEW_LOOP_ORCHESTRATOR": true,
				"FILER_ENABLED":            true,
				"WORKER_PROVISIONED":       false,
			},
		},
		{
			name: "agents template present but neither filer nor worker keyed",
			env: Env{
				AgentsJSONTemplate: `{"reviewer":{"model":"m"}}`,
			},
			want: map[string]bool{
				"FILER_ENABLED":      false,
				"WORKER_PROVISIONED": false,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(tc.env)
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(%+v)[%q] = %v, want %v", tc.env, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesIssueTrackerReadAxis covers the issue-read step gate
// (entrypoint.sh: 801-814, 891-904): exactly one of ISSUE_TRACKER_GITHUB/
// ISSUE_TRACKER_LOCAL/ISSUE_TRACKER_FORGEJO is ever on, selected by
// ISSUE_TRACKER (defaulting to "github" when empty); jira shares github's
// arm since it rides the same in-box reachability.
func TestGatesIssueTrackerReadAxis(t *testing.T) {
	cases := []struct {
		name         string
		issueTracker string
		want         map[string]bool
	}{
		{
			name:         "empty defaults to github",
			issueTracker: "",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  true,
				"ISSUE_TRACKER_LOCAL":   false,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
		{
			name:         "github explicit",
			issueTracker: "github",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  true,
				"ISSUE_TRACKER_LOCAL":   false,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
		{
			name:         "jira rides the github arm",
			issueTracker: "jira",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  true,
				"ISSUE_TRACKER_LOCAL":   false,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
		{
			name:         "local",
			issueTracker: "local",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  false,
				"ISSUE_TRACKER_LOCAL":   true,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
		{
			name:         "forgejo",
			issueTracker: "forgejo",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  false,
				"ISSUE_TRACKER_LOCAL":   false,
				"ISSUE_TRACKER_FORGEJO": true,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{IssueTracker: tc.issueTracker})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(IssueTracker=%q)[%q] = %v, want %v", tc.issueTracker, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesIssueTrackerWriteAxis covers the issue-blocked-comment/
// research-verdict write-step gates (entrypoint.sh: 906-938): a tracker
// with a direct write-step path (github/jira via GITHUB, forgejo via
// FORGEJO) forks on BOX_WRITE_ENABLED between the _READWRITE and _READONLY
// arm; local has no direct write-step path at all (_it_write is empty for
// it, entrypoint.sh: 811), so it renders neither pair regardless of
// BOX_WRITE_ENABLED.
func TestGatesIssueTrackerWriteAxis(t *testing.T) {
	cases := []struct {
		name            string
		issueTracker    string
		boxWriteEnabled bool
		want            map[string]bool
	}{
		{
			name:            "github read-write",
			issueTracker:    "github",
			boxWriteEnabled: true,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  true,
				"ISSUE_TRACKER_GITHUB_READONLY":   false,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  false,
			},
		},
		{
			name:            "github read-only",
			issueTracker:    "github",
			boxWriteEnabled: false,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  false,
				"ISSUE_TRACKER_GITHUB_READONLY":   true,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  false,
			},
		},
		{
			name:            "forgejo read-write",
			issueTracker:    "forgejo",
			boxWriteEnabled: true,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  false,
				"ISSUE_TRACKER_GITHUB_READONLY":   false,
				"ISSUE_TRACKER_FORGEJO_READWRITE": true,
				"ISSUE_TRACKER_FORGEJO_READONLY":  false,
			},
		},
		{
			name:            "forgejo read-only",
			issueTracker:    "forgejo",
			boxWriteEnabled: false,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  false,
				"ISSUE_TRACKER_GITHUB_READONLY":   false,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  true,
			},
		},
		{
			name:            "local has no direct write-step path, write-enabled or not",
			issueTracker:    "local",
			boxWriteEnabled: true,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  false,
				"ISSUE_TRACKER_GITHUB_READONLY":   false,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  false,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{IssueTracker: tc.issueTracker, BoxWriteEnabled: tc.boxWriteEnabled})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(IssueTracker=%q, BoxWriteEnabled=%v)[%q] = %v, want %v", tc.issueTracker, tc.boxWriteEnabled, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesFilerWriteMechanism covers the filer's write-mechanism gates
// (entrypoint.sh: 816-860): relay only activates on read-only
// (BOX_WRITE_ENABLED absent) + the orchestrator gate; every other
// combination keeps the direct gh/fj path, which itself forks on
// ISSUE_TRACKER's filer suffix (_it_filer: GH for github/jira/local,
// FORGEJO for forgejo). Both direct/relay stay off entirely when the filer
// isn't configured. FILER_FILE_DIRECT_ANY fires whenever either direct
// fork is on.
func TestGatesFilerWriteMechanism(t *testing.T) {
	filerTemplate := `{"filer":{"model":"m"}}`
	cases := []struct {
		name                string
		agentsJSONTemplate  string
		issueTracker        string
		boxWriteEnabled     bool
		orchestratorEnabled bool
		want                map[string]bool
	}{
		{
			name:               "filer not configured: everything off",
			agentsJSONTemplate: "",
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      false,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          false,
				"FILER_FILE_DIRECT_ANY":     false,
			},
		},
		{
			name:                "read-only + orchestrator on: relay",
			agentsJSONTemplate:  filerTemplate,
			issueTracker:        "github",
			boxWriteEnabled:     false,
			orchestratorEnabled: true,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      false,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          true,
				"FILER_FILE_DIRECT_ANY":     false,
			},
		},
		{
			name:                "read-write + orchestrator on: direct gh (github tracker)",
			agentsJSONTemplate:  filerTemplate,
			issueTracker:        "github",
			boxWriteEnabled:     true,
			orchestratorEnabled: true,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      true,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          false,
				"FILER_FILE_DIRECT_ANY":     true,
			},
		},
		{
			name:                "read-only + orchestrator off: direct gh (github tracker)",
			agentsJSONTemplate:  filerTemplate,
			issueTracker:        "github",
			boxWriteEnabled:     false,
			orchestratorEnabled: false,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      true,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          false,
				"FILER_FILE_DIRECT_ANY":     true,
			},
		},
		{
			name:                "read-write + orchestrator on: direct forgejo (forgejo tracker)",
			agentsJSONTemplate:  filerTemplate,
			issueTracker:        "forgejo",
			boxWriteEnabled:     true,
			orchestratorEnabled: true,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      false,
				"FILER_FILE_DIRECT_FORGEJO": true,
				"FILER_FILE_RELAY":          false,
				"FILER_FILE_DIRECT_ANY":     true,
			},
		},
		{
			name:                "local tracker's filer suffix rides GH",
			agentsJSONTemplate:  filerTemplate,
			issueTracker:        "local",
			boxWriteEnabled:     true,
			orchestratorEnabled: true,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      true,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          false,
				"FILER_FILE_DIRECT_ANY":     true,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{
				AgentsJSONTemplate:  tc.agentsJSONTemplate,
				IssueTracker:        tc.issueTracker,
				BoxWriteEnabled:     tc.boxWriteEnabled,
				OrchestratorEnabled: tc.orchestratorEnabled,
			})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(%+v)[%q] = %v, want %v", tc, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesPRBodyReference covers the PR-body ticket-reference gates
// (entrypoint.sh: 862-889): exactly one of PR_BODY_CLOSES/PR_BODY_LOCAL_REF/
// PR_BODY_LOCAL_NOREF is ever on, picked from ISSUE_TRACKER x
// LOCAL_ISSUE_REFERENCE. github (and jira, which falls into the same else
// branch) always keeps PR_BODY_CLOSES; local's default is PR_BODY_LOCAL_
// NOREF, and local's opt-in (LOCAL_ISSUE_REFERENCE set) swaps in
// PR_BODY_LOCAL_REF.
func TestGatesPRBodyReference(t *testing.T) {
	cases := []struct {
		name                string
		issueTracker        string
		localIssueReference bool
		want                map[string]bool
	}{
		{
			name:         "github always closes",
			issueTracker: "github",
			want: map[string]bool{
				"PR_BODY_CLOSES":      true,
				"PR_BODY_LOCAL_REF":   false,
				"PR_BODY_LOCAL_NOREF": false,
			},
		},
		{
			name:                "github ignores LOCAL_ISSUE_REFERENCE",
			issueTracker:        "github",
			localIssueReference: true,
			want: map[string]bool{
				"PR_BODY_CLOSES":      true,
				"PR_BODY_LOCAL_REF":   false,
				"PR_BODY_LOCAL_NOREF": false,
			},
		},
		{
			name:         "jira falls into the same else branch as github",
			issueTracker: "jira",
			want: map[string]bool{
				"PR_BODY_CLOSES":      true,
				"PR_BODY_LOCAL_REF":   false,
				"PR_BODY_LOCAL_NOREF": false,
			},
		},
		{
			name:         "local default: no reference",
			issueTracker: "local",
			want: map[string]bool{
				"PR_BODY_CLOSES":      false,
				"PR_BODY_LOCAL_REF":   false,
				"PR_BODY_LOCAL_NOREF": true,
			},
		},
		{
			name:                "local opt-in: breadcrumb reference",
			issueTracker:        "local",
			localIssueReference: true,
			want: map[string]bool{
				"PR_BODY_CLOSES":      false,
				"PR_BODY_LOCAL_REF":   true,
				"PR_BODY_LOCAL_NOREF": false,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{IssueTracker: tc.issueTracker, LocalIssueReference: tc.localIssueReference})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(IssueTracker=%q, LocalIssueReference=%v)[%q] = %v, want %v", tc.issueTracker, tc.localIssueReference, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesBoxAccess covers the OPEN A PULL REQUEST push step gate
// (entrypoint.sh: 940-957): exactly one of BOX_ACCESS_READ_WRITE/
// BOX_ACCESS_READ_ONLY is ever on, selected solely by BOX_WRITE_ENABLED --
// independent of ISSUE_TRACKER/CODE_FORGE.
func TestGatesBoxAccess(t *testing.T) {
	cases := []struct {
		name            string
		boxWriteEnabled bool
		want            map[string]bool
	}{
		{
			name:            "write-enabled",
			boxWriteEnabled: true,
			want: map[string]bool{
				"BOX_ACCESS_READ_WRITE": true,
				"BOX_ACCESS_READ_ONLY":  false,
			},
		},
		{
			name:            "read-only",
			boxWriteEnabled: false,
			want: map[string]bool{
				"BOX_ACCESS_READ_WRITE": false,
				"BOX_ACCESS_READ_ONLY":  true,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{BoxWriteEnabled: tc.boxWriteEnabled})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(BoxWriteEnabled=%v)[%q] = %v, want %v", tc.boxWriteEnabled, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesCodeForgeBackend covers the CODE_FORGE-backend gate family
// (entrypoint.sh: 958-989): CODE_FORGE (defaulting to "github" when empty)
// resolves to a GH or FORGEJO backend suffix, only forgejo diverging from
// the shared gh-flavored path. OPEN_PR_CREATE_RW_<suffix> forks further on
// BOX_ACCESS_READ_WRITE (only the read-write create step splits on
// CODE_FORGE); FIX_CI_READ_<suffix> fires unconditionally on the resolved
// backend, regardless of box access.
func TestGatesCodeForgeBackend(t *testing.T) {
	cases := []struct {
		name            string
		codeForge       string
		boxWriteEnabled bool
		want            map[string]bool
	}{
		{
			name:            "empty defaults to github, read-write",
			codeForge:       "",
			boxWriteEnabled: true,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      true,
				"OPEN_PR_CREATE_RW_FORGEJO": false,
				"FIX_CI_READ_GH":            true,
				"FIX_CI_READ_FORGEJO":       false,
			},
		},
		{
			name:            "github explicit, read-write",
			codeForge:       "github",
			boxWriteEnabled: true,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      true,
				"OPEN_PR_CREATE_RW_FORGEJO": false,
				"FIX_CI_READ_GH":            true,
				"FIX_CI_READ_FORGEJO":       false,
			},
		},
		{
			name:            "github explicit, read-only: OPEN_PR_CREATE_RW off, FIX_CI_READ still on",
			codeForge:       "github",
			boxWriteEnabled: false,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      false,
				"OPEN_PR_CREATE_RW_FORGEJO": false,
				"FIX_CI_READ_GH":            true,
				"FIX_CI_READ_FORGEJO":       false,
			},
		},
		{
			name:            "forgejo, read-write",
			codeForge:       "forgejo",
			boxWriteEnabled: true,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      false,
				"OPEN_PR_CREATE_RW_FORGEJO": true,
				"FIX_CI_READ_GH":            false,
				"FIX_CI_READ_FORGEJO":       true,
			},
		},
		{
			name:            "forgejo, read-only: OPEN_PR_CREATE_RW off, FIX_CI_READ still on",
			codeForge:       "forgejo",
			boxWriteEnabled: false,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      false,
				"OPEN_PR_CREATE_RW_FORGEJO": false,
				"FIX_CI_READ_GH":            false,
				"FIX_CI_READ_FORGEJO":       true,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{CodeForge: tc.codeForge, BoxWriteEnabled: tc.boxWriteEnabled})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(CodeForge=%q, BoxWriteEnabled=%v)[%q] = %v, want %v", tc.codeForge, tc.boxWriteEnabled, k, got[k], want)
				}
			}
		})
	}
}
