package promptassembly

import "testing"

// TestGatesIssueTrackerReadAxis covers the issue-read step gate
// (entrypoint.sh: 801-814, 891-904): exactly one of ISSUE_TRACKER_GITHUB/
// ISSUE_TRACKER_LOCAL/ISSUE_TRACKER_FORGEJO is ever on, selected by the
// pre-resolved TrackerAxisRead rather than re-derived by Gates (issue #2533).
// Jira uses the github arm because it has the same in-box reachability.
func TestGatesIssueTrackerReadAxis(t *testing.T) {
	cases := []struct {
		name            string
		trackerAxisRead string
		issueTracker    string
		want            map[string]bool
	}{
		{
			name:            "empty ISSUE_TRACKER resolves upstream to GITHUB",
			trackerAxisRead: "GITHUB",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  true,
				"ISSUE_TRACKER_LOCAL":   false,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
		{
			name:            "github explicit",
			trackerAxisRead: "GITHUB",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  true,
				"ISSUE_TRACKER_LOCAL":   false,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
		{
			name:            "jira rides the github arm",
			trackerAxisRead: "GITHUB",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  true,
				"ISSUE_TRACKER_LOCAL":   false,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
		{
			name:            "local",
			trackerAxisRead: "LOCAL",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  false,
				"ISSUE_TRACKER_LOCAL":   true,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
		{
			name:            "forgejo",
			trackerAxisRead: "FORGEJO",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  false,
				"ISSUE_TRACKER_LOCAL":   false,
				"ISSUE_TRACKER_FORGEJO": true,
			},
		},
		{
			// A host launcher predating issue #2533 never sets
			// BOX_TRACKER_AXIS_READ and no baked default covers it, so
			// TrackerAxisRead arrives empty against a newer box image.
			// Gates must fail open to the old "${ISSUE_TRACKER:-github}"
			// arm instead of dropping every tracker-gated fragment.
			name:            "empty TrackerAxisRead falls open to GITHUB defaults",
			trackerAxisRead: "",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  true,
				"ISSUE_TRACKER_LOCAL":   false,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
		{
			// Same version-skew shape, but IssueTracker says "local". The
			// fallback must re-derive from IssueTracker (issue #2533
			// review): hardcoding the github arm would render
			// ISSUE_TRACKER_GITHUB alongside the PR_BODY_LOCAL_NOREF that
			// the PR-body gate below correctly picks.
			name:            "empty TrackerAxisRead with IssueTracker=local falls open to LOCAL",
			trackerAxisRead: "",
			issueTracker:    "local",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  false,
				"ISSUE_TRACKER_LOCAL":   true,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
		{
			name:            "empty TrackerAxisRead with IssueTracker=forgejo falls open to FORGEJO",
			trackerAxisRead: "",
			issueTracker:    "forgejo",
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
			got := Gates(Env{TrackerAxisRead: tc.trackerAxisRead, IssueTracker: tc.issueTracker})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(TrackerAxisRead=%q, IssueTracker=%q)[%q] = %v, want %v", tc.trackerAxisRead, tc.issueTracker, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesIssueTrackerWriteAxis covers the issue-blocked-comment and
// research-verdict write-step gates (entrypoint.sh: 906-938): github and
// forgejo fork on BOX_WRITE_ENABLED, local has no direct write path at all.
// Every case sets a non-empty TrackerAxisRead so the itRead=="" version-skew
// fallback (issue #2533), covered in TestGatesIssueTrackerReadAxis, stays off.
func TestGatesIssueTrackerWriteAxis(t *testing.T) {
	cases := []struct {
		name             string
		trackerAxisRead  string
		trackerAxisWrite string
		boxWriteEnabled  bool
		want             map[string]bool
	}{
		{
			name:             "github read-write",
			trackerAxisRead:  "GITHUB",
			trackerAxisWrite: "GITHUB",
			boxWriteEnabled:  true,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  true,
				"ISSUE_TRACKER_GITHUB_READONLY":   false,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  false,
			},
		},
		{
			name:             "github read-only",
			trackerAxisRead:  "GITHUB",
			trackerAxisWrite: "GITHUB",
			boxWriteEnabled:  false,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  false,
				"ISSUE_TRACKER_GITHUB_READONLY":   true,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  false,
			},
		},
		{
			name:             "forgejo read-write",
			trackerAxisRead:  "FORGEJO",
			trackerAxisWrite: "FORGEJO",
			boxWriteEnabled:  true,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  false,
				"ISSUE_TRACKER_GITHUB_READONLY":   false,
				"ISSUE_TRACKER_FORGEJO_READWRITE": true,
				"ISSUE_TRACKER_FORGEJO_READONLY":  false,
			},
		},
		{
			name:             "forgejo read-only",
			trackerAxisRead:  "FORGEJO",
			trackerAxisWrite: "FORGEJO",
			boxWriteEnabled:  false,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  false,
				"ISSUE_TRACKER_GITHUB_READONLY":   false,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  true,
			},
		},
		{
			name:             "local has no direct write-step path, write-enabled or not",
			trackerAxisRead:  "LOCAL",
			trackerAxisWrite: "",
			boxWriteEnabled:  true,
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
			got := Gates(Env{TrackerAxisRead: tc.trackerAxisRead, TrackerAxisWrite: tc.trackerAxisWrite, BoxWriteEnabled: tc.boxWriteEnabled})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(TrackerAxisRead=%q, TrackerAxisWrite=%q, BoxWriteEnabled=%v)[%q] = %v, want %v", tc.trackerAxisRead, tc.trackerAxisWrite, tc.boxWriteEnabled, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesIssueTrackerWriteAxisResearch covers the research special-case
// (ADR 0041 / issue #2593) on top of the write-step gates above: a research
// dispatch with the Filer provisioned always forces the _READONLY arm
// regardless of BOX_WRITE_ENABLED, because the research-verdict fragments
// share these four gates with the work-path issue-blocked-comment ones.
func TestGatesIssueTrackerWriteAxisResearch(t *testing.T) {
	cases := []struct {
		name             string
		filerEnabled     bool
		trackerAxisRead  string
		trackerAxisWrite string
		boxWriteEnabled  bool
		want             map[string]bool
	}{
		{
			name:             "research + filer + github read-write forces READONLY",
			filerEnabled:     true,
			trackerAxisRead:  "GITHUB",
			trackerAxisWrite: "GITHUB",
			boxWriteEnabled:  true,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  false,
				"ISSUE_TRACKER_GITHUB_READONLY":   true,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  false,
			},
		},
		{
			name:             "research + filer + github read-only stays READONLY",
			filerEnabled:     true,
			trackerAxisRead:  "GITHUB",
			trackerAxisWrite: "GITHUB",
			boxWriteEnabled:  false,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  false,
				"ISSUE_TRACKER_GITHUB_READONLY":   true,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  false,
			},
		},
		{
			name:             "research + filer + forgejo read-write forces READONLY",
			filerEnabled:     true,
			trackerAxisRead:  "FORGEJO",
			trackerAxisWrite: "FORGEJO",
			boxWriteEnabled:  true,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  false,
				"ISSUE_TRACKER_GITHUB_READONLY":   false,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  true,
			},
		},
		{
			name:             "research without filer renders exactly as today (read-write)",
			filerEnabled:     false,
			trackerAxisRead:  "GITHUB",
			trackerAxisWrite: "GITHUB",
			boxWriteEnabled:  true,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  true,
				"ISSUE_TRACKER_GITHUB_READONLY":   false,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  false,
			},
		},
		{
			name:             "research without filer renders exactly as today (read-only)",
			filerEnabled:     false,
			trackerAxisRead:  "GITHUB",
			trackerAxisWrite: "GITHUB",
			boxWriteEnabled:  false,
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB_READWRITE":  false,
				"ISSUE_TRACKER_GITHUB_READONLY":   true,
				"ISSUE_TRACKER_FORGEJO_READWRITE": false,
				"ISSUE_TRACKER_FORGEJO_READONLY":  false,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := Env{
				DispatchKind:     "research",
				FilerEnabled:     tc.filerEnabled,
				TrackerAxisRead:  tc.trackerAxisRead,
				TrackerAxisWrite: tc.trackerAxisWrite,
				BoxWriteEnabled:  tc.boxWriteEnabled,
			}
			got := Gates(env)
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(%+v)[%q] = %v, want %v", env, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesFilerWriteMechanism covers the filer's write-mechanism gates
// (entrypoint.sh: 816-860): relay only activates on read-only plus the
// orchestrator gate, and every other combination keeps the direct gh/fj
// path, which forks on TrackerAxisFiler. Every case sets a non-empty
// TrackerAxisRead so the itRead=="" version-skew fallback (issue #2533) stays off.
func TestGatesFilerWriteMechanism(t *testing.T) {
	cases := []struct {
		name                string
		filerEnabled        bool
		trackerAxisRead     string
		trackerAxisFiler    string
		boxWriteEnabled     bool
		orchestratorEnabled bool
		want                map[string]bool
	}{
		{
			name:            "filer not configured: everything off",
			filerEnabled:    false,
			trackerAxisRead: "GITHUB",
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      false,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          false,
				"FILER_FILE_RELAY_WORK":     false,
				"FILER_FILE_RELAY_RESEARCH": false,
				"FILER_FILE_DIRECT_ANY":     false,
			},
		},
		{
			name:                "read-only + orchestrator on: relay",
			filerEnabled:        true,
			trackerAxisRead:     "GITHUB",
			trackerAxisFiler:    "GH",
			boxWriteEnabled:     false,
			orchestratorEnabled: true,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      false,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          true,
				"FILER_FILE_RELAY_WORK":     true,
				"FILER_FILE_RELAY_RESEARCH": false,
				"FILER_FILE_DIRECT_ANY":     false,
			},
		},
		{
			name:                "read-write + orchestrator on: direct gh (github tracker)",
			filerEnabled:        true,
			trackerAxisRead:     "GITHUB",
			trackerAxisFiler:    "GH",
			boxWriteEnabled:     true,
			orchestratorEnabled: true,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      true,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          false,
				"FILER_FILE_RELAY_WORK":     false,
				"FILER_FILE_RELAY_RESEARCH": false,
				"FILER_FILE_DIRECT_ANY":     true,
			},
		},
		{
			name:                "read-only + orchestrator off: direct gh (github tracker)",
			filerEnabled:        true,
			trackerAxisRead:     "GITHUB",
			trackerAxisFiler:    "GH",
			boxWriteEnabled:     false,
			orchestratorEnabled: false,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      true,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          false,
				"FILER_FILE_RELAY_WORK":     false,
				"FILER_FILE_RELAY_RESEARCH": false,
				"FILER_FILE_DIRECT_ANY":     true,
			},
		},
		{
			name:                "read-write + orchestrator on: direct forgejo (forgejo tracker)",
			filerEnabled:        true,
			trackerAxisRead:     "FORGEJO",
			trackerAxisFiler:    "FORGEJO",
			boxWriteEnabled:     true,
			orchestratorEnabled: true,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      false,
				"FILER_FILE_DIRECT_FORGEJO": true,
				"FILER_FILE_RELAY":          false,
				"FILER_FILE_RELAY_WORK":     false,
				"FILER_FILE_RELAY_RESEARCH": false,
				"FILER_FILE_DIRECT_ANY":     true,
			},
		},
		{
			name:                "local tracker's filer suffix rides GH",
			filerEnabled:        true,
			trackerAxisRead:     "LOCAL",
			trackerAxisFiler:    "GH",
			boxWriteEnabled:     true,
			orchestratorEnabled: true,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      true,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          false,
				"FILER_FILE_RELAY_WORK":     false,
				"FILER_FILE_RELAY_RESEARCH": false,
				"FILER_FILE_DIRECT_ANY":     true,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{
				FilerEnabled:        tc.filerEnabled,
				TrackerAxisRead:     tc.trackerAxisRead,
				TrackerAxisFiler:    tc.trackerAxisFiler,
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

// TestGatesFilerWriteMechanismResearch covers the research special-case
// (ADR 0041 / issue #2593): a research dispatch with the Filer provisioned
// always relays, with no orchestrator condition and regardless of
// BOX_WRITE_ENABLED. Relay fires even in read-write mode with the
// orchestrator off, which the pre-#2593 work-path rule would never produce.
func TestGatesFilerWriteMechanismResearch(t *testing.T) {
	cases := []struct {
		name                string
		filerEnabled        bool
		trackerAxisRead     string
		trackerAxisFiler    string
		boxWriteEnabled     bool
		orchestratorEnabled bool
		want                map[string]bool
	}{
		{
			name:                "read-write + orchestrator off: still relay",
			filerEnabled:        true,
			trackerAxisRead:     "GITHUB",
			trackerAxisFiler:    "GH",
			boxWriteEnabled:     true,
			orchestratorEnabled: false,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      false,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          true,
				"FILER_FILE_RELAY_WORK":     false,
				"FILER_FILE_RELAY_RESEARCH": true,
				"FILER_FILE_DIRECT_ANY":     false,
			},
		},
		{
			name:                "read-write + orchestrator on: still relay (no orchestrator condition)",
			filerEnabled:        true,
			trackerAxisRead:     "GITHUB",
			trackerAxisFiler:    "GH",
			boxWriteEnabled:     true,
			orchestratorEnabled: true,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      false,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          true,
				"FILER_FILE_RELAY_WORK":     false,
				"FILER_FILE_RELAY_RESEARCH": true,
				"FILER_FILE_DIRECT_ANY":     false,
			},
		},
		{
			name:                "read-only + orchestrator off: still relay",
			filerEnabled:        true,
			trackerAxisRead:     "GITHUB",
			trackerAxisFiler:    "GH",
			boxWriteEnabled:     false,
			orchestratorEnabled: false,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      false,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          true,
				"FILER_FILE_RELAY_WORK":     false,
				"FILER_FILE_RELAY_RESEARCH": true,
				"FILER_FILE_DIRECT_ANY":     false,
			},
		},
		{
			name:            "filer not configured: research special-case never fires",
			filerEnabled:    false,
			trackerAxisRead: "GITHUB",
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      false,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          false,
				"FILER_FILE_RELAY_WORK":     false,
				"FILER_FILE_RELAY_RESEARCH": false,
				"FILER_FILE_DIRECT_ANY":     false,
			},
		},
		{
			name:                "forgejo tracker + filer: relay, not direct-forgejo",
			filerEnabled:        true,
			trackerAxisRead:     "FORGEJO",
			trackerAxisFiler:    "FORGEJO",
			boxWriteEnabled:     true,
			orchestratorEnabled: true,
			want: map[string]bool{
				"FILER_FILE_DIRECT_GH":      false,
				"FILER_FILE_DIRECT_FORGEJO": false,
				"FILER_FILE_RELAY":          true,
				"FILER_FILE_RELAY_WORK":     false,
				"FILER_FILE_RELAY_RESEARCH": true,
				"FILER_FILE_DIRECT_ANY":     false,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := Env{
				DispatchKind:        "research",
				FilerEnabled:        tc.filerEnabled,
				TrackerAxisRead:     tc.trackerAxisRead,
				TrackerAxisFiler:    tc.trackerAxisFiler,
				BoxWriteEnabled:     tc.boxWriteEnabled,
				OrchestratorEnabled: tc.orchestratorEnabled,
			}
			got := Gates(env)
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(%+v)[%q] = %v, want %v", env, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesPRBodyReference covers the PR-body ticket-reference gates
// (entrypoint.sh: 862-889): exactly one of PR_BODY_CLOSES/PR_BODY_LOCAL_REF/
// PR_BODY_LOCAL_NOREF is ever on, picked from ISSUE_TRACKER x
// LOCAL_ISSUE_REFERENCE. Jira falls into github's else branch.
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
