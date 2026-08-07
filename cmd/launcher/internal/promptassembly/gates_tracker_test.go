package promptassembly

import "testing"

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
