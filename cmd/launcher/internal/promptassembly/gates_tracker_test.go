package promptassembly

import "testing"

// TestGatesIssueTrackerReadAxis covers the issue-read step gate
// (entrypoint.sh: 801-814, 891-904): exactly one of ISSUE_TRACKER_GITHUB/
// ISSUE_TRACKER_LOCAL/ISSUE_TRACKER_FORGEJO is ever on, selected by
// TrackerAxisRead -- nix's precomputed equivalent of ISSUE_TRACKER
// (defaulting to "github" when empty; jira sharing github's arm since it
// rides the same in-box reachability), resolved upstream and carried
// pre-resolved rather than re-derived by Gates itself (issue #2533).
func TestGatesIssueTrackerReadAxis(t *testing.T) {
	cases := []struct {
		name            string
		trackerAxisRead string
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
			// This pins that fail-open contract so a future change can't
			// silently reintroduce the fail-closed regression.
			name:            "empty TrackerAxisRead falls open to GITHUB defaults",
			trackerAxisRead: "",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  true,
				"ISSUE_TRACKER_LOCAL":   false,
				"ISSUE_TRACKER_FORGEJO": false,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{TrackerAxisRead: tc.trackerAxisRead})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(TrackerAxisRead=%q)[%q] = %v, want %v", tc.trackerAxisRead, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesIssueTrackerWriteAxis covers the issue-blocked-comment/
// research-verdict write-step gates (entrypoint.sh: 906-938): a tracker
// with a direct write-step path (github/jira via GITHUB, forgejo via
// FORGEJO) forks on BOX_WRITE_ENABLED between the _READWRITE and _READONLY
// arm; local has no direct write-step path at all (TrackerAxisWrite is ""
// for it, entrypoint.sh: 811), so it renders neither pair regardless of
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

// TestGatesFilerWriteMechanism covers the filer's write-mechanism gates
// (entrypoint.sh: 816-860): relay only activates on read-only
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
