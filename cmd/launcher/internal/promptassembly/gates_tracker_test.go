package promptassembly

import "testing"

// TestGatesIssueTrackerReadAxis covers the issue-read step gate
// (entrypoint.sh, 891-904): exactly one of ISSUE_TRACKER_GITHUB/
// ISSUE_TRACKER_LOCAL/ISSUE_TRACKER_FORGEJO is ever on, selected by
// TrackerAxisRead -- nix's precomputed equivalent of ISSUE_TRACKER
// (defaulting to "github" when empty; jira sharing github's arm since it
// rides the same in-box reachability), resolved upstream and carried
// pre-resolved rather than re-derived by Gates itself (issue #2533).
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
			// TrackerAxisRead's zero value: the shape a version-skew
			// dispatch leaves behind, not just a stray `Env{}` literal.
			// BOX_TRACKER_AXIS_READ/WRITE/FILER are dispatch-time-only
			// forwards (issue #2533) with no baked preamble default, so an
			// older host launcher binary that predates issue #2533 (and
			// therefore never sets these env vars at all) dispatching
			// against a newer box image leaves TrackerAxisRead empty here
			// even though the tracker gate family is fully wired up.
			// Before issue #2533, entrypoint.sh's own bash
			// "${ISSUE_TRACKER:-github}" defaulting guaranteed a real gate
			// fired regardless; Gates now reproduces that same default arm
			// as a version-skew safety net so an old-launcher/new-box
			// pairing renders the github/jira arm instead of silently
			// dropping every tracker-gated prompt fragment for the run.
			name:            "empty TrackerAxisRead falls open to GITHUB defaults",
			trackerAxisRead: "",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  true,
				"ISSUE_TRACKER_LOCAL":   false,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
		{
			// Same version-skew shape as above, but IssueTracker itself --
			// still forwarded on Env for exactly this fallback (env.go:
			// 93-101) -- says "local". The fallback must re-derive from
			// IssueTracker, not hardcode the github/jira arm regardless of
			// it (issue #2533 review): hardcoding GITHUB here would render
			// the self-contradictory ISSUE_TRACKER_GITHUB alongside
			// PR_BODY_LOCAL_NOREF (the PR-body gate below, which already
			// reads IssueTracker directly and would correctly pick local).
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

// TestGatesIssueTrackerWriteAxis covers the issue-blocked-comment/
// research-verdict write-step gates (entrypoint.sh): a tracker
// with a direct write-step path (github/jira via GITHUB, forgejo via
// FORGEJO) forks on BOX_WRITE_ENABLED between the _READWRITE and _READONLY
// arm; local has no direct write-step path at all (TrackerAxisWrite is ""
// for it, entrypoint.sh), so it renders neither pair regardless of
// BOX_WRITE_ENABLED. TrackerAxisWrite arrives pre-resolved from nix (issue
// #2533) rather than being re-derived here from ISSUE_TRACKER. Each case
// also sets a non-empty TrackerAxisRead matching the tracker under test --
// the shape a real nix-resolved Env always carries -- so trackerGates'
// itRead=="" version-skew fallback (which defaults itWrite along with
// itRead) never fires here; that fallback gets its own dedicated coverage
// in TestGatesIssueTrackerReadAxis.
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
// (ADR 0041 / issue #2593) layered on top of the write-step gates covered
// by TestGatesIssueTrackerWriteAxis above: a research dispatch with the
// Filer provisioned always forces the _READONLY arm -- never _READWRITE --
// regardless of BOX_WRITE_ENABLED, since research-verdict-github(-readonly).md
// shares these same four gates with the work-path issue-blocked-comment
// fragments. Without the Filer provisioned, research renders exactly as a
// work dispatch would.
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
// (entrypoint.sh): relay only activates on read-only
// (BOX_WRITE_ENABLED absent) + the orchestrator gate; every other
// combination keeps the direct gh/fj path, which itself forks on
// TrackerAxisFiler (GH for github/jira/local, FORGEJO for forgejo -- nix's
// precomputed equivalent of ISSUE_TRACKER's filer suffix, issue #2533).
// Both direct/relay stay off entirely when the filer isn't configured
// (Env.FilerEnabled false, nix's precomputed roster fact rather than a
// reparsed AgentsJSONTemplate). FILER_FILE_DIRECT_ANY fires whenever either
// direct fork is on. Each case also sets a non-empty TrackerAxisRead
// matching the tracker under test -- the shape a real nix-resolved Env
// always carries -- so trackerGates' itRead=="" version-skew fallback
// (which defaults itFiler along with itRead) never fires here; that
// fallback gets its own dedicated coverage in TestGatesIssueTrackerReadAxis.
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
// (ADR 0041 / issue #2593) layered on top of TestGatesFilerWriteMechanism
// above: a research dispatch with the Filer provisioned always relays --
// never direct-gh, never direct-forgejo -- with no orchestrator condition
// and regardless of BOX_WRITE_ENABLED. This is the acceptance-criterion-
// critical shape: relay fires even in read-write mode with orchestrator
// off, which the pre-#2593 work-path rule would never produce.
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
// (entrypoint.sh): exactly one of PR_BODY_CLOSES/PR_BODY_LOCAL_REF/
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
