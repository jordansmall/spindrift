package promptassembly

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
)

// TestGatesIssueTrackerReadAxis covers the issue-read step gate: exactly one of
// ISSUE_TRACKER_GITHUB/ISSUE_TRACKER_LOCAL/ISSUE_TRACKER_FORGEJO is ever on,
// selected by the pre-resolved TrackerAxisRead rather than re-derived by Gates
// (issue #2533).
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
// research-verdict write-step gates: github and forgejo fork on
// BOX_WRITE_ENABLED, local has no direct write path at all.
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

// TestGatesFilerWriteMechanism covers the filer's write-mechanism gates: relay
// only activates on read-only access, and a writable Box keeps the direct
// gh/fj path, which forks on TrackerAxisFiler. Every case sets a non-empty
// TrackerAxisRead so the itRead=="" version-skew fallback (issue #2533) stays off.
func TestGatesFilerWriteMechanism(t *testing.T) {
	cases := []struct {
		name             string
		filerEnabled     bool
		trackerAxisRead  string
		trackerAxisFiler string
		boxWriteEnabled  bool
		want             map[string]bool
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
			name:             "read-only: relay",
			filerEnabled:     true,
			trackerAxisRead:  "GITHUB",
			trackerAxisFiler: "GH",
			boxWriteEnabled:  false,
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
			name:             "read-write: direct gh (github tracker)",
			filerEnabled:     true,
			trackerAxisRead:  "GITHUB",
			trackerAxisFiler: "GH",
			boxWriteEnabled:  true,
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
			name:             "read-write: direct forgejo (forgejo tracker)",
			filerEnabled:     true,
			trackerAxisRead:  "FORGEJO",
			trackerAxisFiler: "FORGEJO",
			boxWriteEnabled:  true,
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
			name:             "local tracker's filer suffix rides GH",
			filerEnabled:     true,
			trackerAxisRead:  "LOCAL",
			trackerAxisFiler: "GH",
			boxWriteEnabled:  true,
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
				FilerEnabled:     tc.filerEnabled,
				TrackerAxisRead:  tc.trackerAxisRead,
				TrackerAxisFiler: tc.trackerAxisFiler,
				BoxWriteEnabled:  tc.boxWriteEnabled,
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
// always relays, regardless of BOX_WRITE_ENABLED. Relay fires even in
// read-write mode, which the work-path rule would never produce.
func TestGatesFilerWriteMechanismResearch(t *testing.T) {
	cases := []struct {
		name             string
		filerEnabled     bool
		trackerAxisRead  string
		trackerAxisFiler string
		boxWriteEnabled  bool
		want             map[string]bool
	}{
		{
			name:             "read-write: still relay",
			filerEnabled:     true,
			trackerAxisRead:  "GITHUB",
			trackerAxisFiler: "GH",
			boxWriteEnabled:  true,
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
			name:             "read-only: still relay",
			filerEnabled:     true,
			trackerAxisRead:  "GITHUB",
			trackerAxisFiler: "GH",
			boxWriteEnabled:  false,
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
			name:             "forgejo tracker + filer: relay, not direct-forgejo",
			filerEnabled:     true,
			trackerAxisRead:  "FORGEJO",
			trackerAxisFiler: "FORGEJO",
			boxWriteEnabled:  true,
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
				DispatchKind:     "research",
				FilerEnabled:     tc.filerEnabled,
				TrackerAxisRead:  tc.trackerAxisRead,
				TrackerAxisFiler: tc.trackerAxisFiler,
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

// TestGatesPRBodyReference covers the PR-body ticket-reference gates: exactly
// one of PR_BODY_CLOSES/PR_BODY_LOCAL_REF/PR_BODY_LOCAL_NOREF is ever on,
// picked from ISSUE_TRACKER x LOCAL_ISSUE_REFERENCE. Jira falls into github's
// else branch.
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

// TestGatesResearchVerdictRelayed pins that RESEARCH_VERDICT_RELAYED is the
// union of the paths that render a relayed research-verdict step (a read-only
// github/forgejo Box, a local tracker, a Filer-forced relay), scoped to the
// verdict contract (ContractVerdict; issue #5130).
func TestGatesResearchVerdictRelayed(t *testing.T) {
	cases := []struct {
		name         string
		kind         string
		filerEnabled bool
		read, write  string
		boxWrite     bool
		want         bool
	}{
		{"github read-write", "research", false, "GITHUB", "GITHUB", true, false},
		{"github read-only", "research", false, "GITHUB", "GITHUB", false, true},
		{"github read-write forced relay by Filer", "research", true, "GITHUB", "GITHUB", true, true},
		{"forgejo read-write", "research", false, "FORGEJO", "FORGEJO", true, false},
		{"forgejo read-only", "research", false, "FORGEJO", "FORGEJO", false, true},
		{"forgejo read-write forced relay by Filer", "research", true, "FORGEJO", "FORGEJO", true, true},
		{"local", "research", false, "LOCAL", "", true, true},
		{"work github read-only", "work", false, "GITHUB", "GITHUB", false, false},
		{"work local", "work", false, "LOCAL", "", true, false},
		{"butler github read-only", "butler", false, "GITHUB", "GITHUB", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := Env{
				DispatchKind:     tc.kind,
				FilerEnabled:     tc.filerEnabled,
				TrackerAxisRead:  tc.read,
				TrackerAxisWrite: tc.write,
				BoxWriteEnabled:  tc.boxWrite,
			}
			if got := Gates(env)["RESEARCH_VERDICT_RELAYED"]; got != tc.want {
				t.Errorf("Gates(%+v)[RESEARCH_VERDICT_RELAYED] = %v, want %v", env, got, tc.want)
			}
		})
	}
}

// TestGatesJiraResearchAlwaysRelays pins that a jira research dispatch uses the
// relayed verdict form with the Filer off or on (issue #5245), while jira stays
// on the GITHUB read/write axis, and a jira work dispatch is not relayed.
func TestGatesJiraResearchAlwaysRelays(t *testing.T) {
	cases := []struct {
		name         string
		kind         string
		filerEnabled bool
		wantReadOnly bool
		wantRelayed  bool
	}{
		{"research, Filer off", "research", false, true, true},
		{"research, Filer on", "research", true, true, true},
		{"work, Filer off", "work", false, false, false},
		{"butler, Filer off", "butler", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := Gates(Env{
				DispatchKind:    tc.kind,
				IssueTracker:    "jira",
				FilerEnabled:    tc.filerEnabled,
				BoxWriteEnabled: true,
			})
			if !g["ISSUE_TRACKER_GITHUB"] {
				t.Error("ISSUE_TRACKER_GITHUB = false, want true (jira stays on the GITHUB axis)")
			}
			if g["ISSUE_TRACKER_GITHUB_READONLY"] != tc.wantReadOnly {
				t.Errorf("ISSUE_TRACKER_GITHUB_READONLY = %v, want %v", g["ISSUE_TRACKER_GITHUB_READONLY"], tc.wantReadOnly)
			}
			if g["ISSUE_TRACKER_GITHUB_READWRITE"] {
				t.Error("ISSUE_TRACKER_GITHUB_READWRITE = true, want false (no in-box jira write path)")
			}
			if g["RESEARCH_VERDICT_RELAYED"] != tc.wantRelayed {
				t.Errorf("RESEARCH_VERDICT_RELAYED = %v, want %v", g["RESEARCH_VERDICT_RELAYED"], tc.wantRelayed)
			}
		})
	}
}

// TestAssembleJiraResearchRendersRelayedVerdict pins the rendered prompt: a jira
// research run has no `gh issue comment` guidance, Filer off or on (issue
// #5245).
func TestAssembleJiraResearchRendersRelayedVerdict(t *testing.T) {
	reg := loadTestRegistry(t)
	for _, selfContained := range []bool{false, true} {
		for _, filer := range []bool{false, true} {
			env := coveredEnv()
			env.IssueTracker = "jira"
			env.DispatchKind = "research"
			env.SelfContained = selfContained
			env.FilerEnabled = filer
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("selfContained=%v filer=%v: Assemble: %v", selfContained, filer, err)
			}
			if strings.Contains(result.Prompt, "gh issue comment") {
				t.Errorf("selfContained=%v filer=%v: jira research prompt contains `gh issue comment`:\n%s", selfContained, filer, result.Prompt)
			}
			const relayed = "the launcher posts your\nrelayed verdict host-side once you exit"
			if !strings.Contains(result.Prompt, relayed) {
				t.Errorf("selfContained=%v filer=%v: jira research prompt lacks the relayed landing=none outcome %q", selfContained, filer, relayed)
			}
		}
	}
}

// TestAssembleJiraWorkHasNoGhIssueComment pins the rendered jira work prompt
// (issue #5263): the IF BLOCKED step relays through the host, never
// `gh issue comment`.
func TestAssembleJiraWorkHasNoGhIssueComment(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.IssueTracker = "jira"
	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("work: Assemble: %v", err)
	}
	if strings.Contains(result.Prompt, "gh issue comment") {
		t.Errorf("jira work prompt contains `gh issue comment`:\n%s", result.Prompt)
	}
}

// TestGatesUnwritableReadWrite pins ISSUE_TRACKER_UNWRITABLE_READWRITE (issue
// #5263): true only where the Box has a write token that cannot reach the
// tracker, and exactly one blocked-comment gate fires per cell. The research
// cell stays off: its forced relay takes the _READONLY arm.
func TestGatesUnwritableReadWrite(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		tracker  string
		read     string
		write    string
		boxWrite bool
		want     bool
	}{
		{"jira read-write", "work", "jira", "GITHUB", "GITHUB", true, true},
		{"jira research read-write", "research", "jira", "GITHUB", "GITHUB", true, false},
		{"jira read-only", "work", "jira", "GITHUB", "GITHUB", false, false},
		{"github read-write", "work", "github", "GITHUB", "GITHUB", true, false},
		{"forgejo read-write", "work", "forgejo", "FORGEJO", "FORGEJO", true, false},
		{"local", "work", "local", "LOCAL", "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := Gates(Env{
				DispatchKind:     tc.kind,
				IssueTracker:     tc.tracker,
				TrackerAxisRead:  tc.read,
				TrackerAxisWrite: tc.write,
				BoxWriteEnabled:  tc.boxWrite,
			})
			if g["ISSUE_TRACKER_UNWRITABLE_READWRITE"] != tc.want {
				t.Errorf("ISSUE_TRACKER_UNWRITABLE_READWRITE = %v, want %v", g["ISSUE_TRACKER_UNWRITABLE_READWRITE"], tc.want)
			}
			n := 0
			for _, k := range []string{
				"ISSUE_TRACKER_GITHUB_READWRITE", "ISSUE_TRACKER_GITHUB_READONLY",
				"ISSUE_TRACKER_LOCAL", "ISSUE_TRACKER_FORGEJO_READWRITE",
				"ISSUE_TRACKER_FORGEJO_READONLY", "ISSUE_TRACKER_UNWRITABLE_READWRITE",
			} {
				if g[k] {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%d blocked-comment gates true, want exactly 1", n)
			}
		})
	}
}

// TestGatesResearchVerdictRelayedCarrierSplit pins that the relayed gate
// splits into exactly one of _LOG/_SOCKET by Signal carrier, and that
// neither half is on when the base gate is off.
func TestGatesResearchVerdictRelayedCarrierSplit(t *testing.T) {
	cases := []struct {
		name            string
		carrier         string
		boxWrite        bool
		wantLog, wantSk bool
	}{
		{"log carrier", "log", false, true, false},
		{"empty carrier means log", "", false, true, false},
		{"socket carrier", "socket", false, false, true},
		{"socket carrier, relay off", "socket", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := Gates(Env{
				DispatchKind:     "research",
				SignalCarrier:    tc.carrier,
				TrackerAxisRead:  "GITHUB",
				TrackerAxisWrite: "GITHUB",
				BoxWriteEnabled:  tc.boxWrite,
			})
			if g["RESEARCH_VERDICT_RELAYED_LOG"] != tc.wantLog || g["RESEARCH_VERDICT_RELAYED_SOCKET"] != tc.wantSk {
				t.Errorf("_LOG/_SOCKET = %v/%v, want %v/%v", g["RESEARCH_VERDICT_RELAYED_LOG"], g["RESEARCH_VERDICT_RELAYED_SOCKET"], tc.wantLog, tc.wantSk)
			}
		})
	}
}

// TestGatesResearchVerdictRelayedMatchesRegistry guards against drift between
// the hand-listed RESEARCH_VERDICT_RELAYED union and the research-verdict-*
// registry rows: it must equal the OR of every non-READWRITE row's gate.
func TestGatesResearchVerdictRelayedMatchesRegistry(t *testing.T) {
	var relayedGates []string
	for _, row := range loadTestRegistry(t).Rows {
		if strings.HasPrefix(row.Fragment, "research-verdict-") && !strings.HasSuffix(row.Gate, "_READWRITE") {
			relayedGates = append(relayedGates, row.Gate)
		}
	}
	if len(relayedGates) == 0 {
		t.Fatal("no relayed research-verdict-* rows found in testdata/registry.json")
	}
	for _, tracker := range []string{"GITHUB", "FORGEJO", "LOCAL"} {
		write := tracker
		if tracker == "LOCAL" {
			write = ""
		}
		for _, boxWrite := range []bool{true, false} {
			for _, filer := range []bool{true, false} {
				for _, carrier := range []string{"log", "socket"} {
					env := Env{
						DispatchKind:     "research",
						FilerEnabled:     filer,
						SignalCarrier:    carrier,
						TrackerAxisRead:  tracker,
						TrackerAxisWrite: write,
						BoxWriteEnabled:  boxWrite,
					}
					g := Gates(env)
					want := false
					for _, name := range relayedGates {
						want = want || g[name]
					}
					if g["RESEARCH_VERDICT_RELAYED"] != want {
						t.Errorf("Gates(%+v)[RESEARCH_VERDICT_RELAYED] = %v, registry gates OR to %v", env, g["RESEARCH_VERDICT_RELAYED"], want)
					}
				}
			}
		}
	}
}

// TestGatesIssueTrackerReachable covers the backend-agnostic gate the scout's
// issue-read slot rides: on for every tracker the Box can reach, off for one
// (local) it cannot.
func TestGatesIssueTrackerReachable(t *testing.T) {
	for tracker, want := range map[string]bool{
		"":        true,
		"github":  true,
		"forgejo": true,
		"jira":    true,
		"local":   false,
	} {
		got := Gates(Env{IssueTracker: tracker, TrackerAxisRead: "GITHUB"})["ISSUE_TRACKER_REACHABLE"]
		if got != want {
			t.Errorf("Gates(IssueTracker=%q)[ISSUE_TRACKER_REACHABLE] = %v, want %v", tracker, got, want)
		}
	}
}

// TestGatesBackendAgnosticReadWriteAndRelayed pins ISSUE_TRACKER_READWRITE and
// ISSUE_TRACKER_RELAYED as the exact unions of the backend-named gates they
// replace on the slot rows, across every tracker, access mode, dispatch kind
// and Filer setting. Each gate keeps its own backend-named siblings, which
// the deprecated rows still read.
func TestGatesBackendAgnosticReadWriteAndRelayed(t *testing.T) {
	for _, tracker := range []string{"github", "forgejo", "local", "jira"} {
		for _, boxWrite := range []bool{true, false} {
			for _, kd := range dispatchkind.All {
				for _, filer := range []bool{true, false} {
					for _, carrier := range []string{"log", "socket"} {
						env := Env{
							IssueTracker:    tracker,
							BoxWriteEnabled: boxWrite,
							DispatchKind:    kd.Name,
							FilerEnabled:    filer,
							SignalCarrier:   carrier,
						}
						g := Gates(env)
						wantRW := g["ISSUE_TRACKER_GITHUB_READWRITE"] || g["ISSUE_TRACKER_FORGEJO_READWRITE"]
						wantRelayed := g["ISSUE_TRACKER_GITHUB_READONLY"] || g["ISSUE_TRACKER_LOCAL"] || g["ISSUE_TRACKER_FORGEJO_READONLY"]
						if g["ISSUE_TRACKER_READWRITE"] != wantRW {
							t.Errorf("Gates(%+v)[ISSUE_TRACKER_READWRITE] = %v, want %v", env, g["ISSUE_TRACKER_READWRITE"], wantRW)
						}
						if g["ISSUE_TRACKER_RELAYED"] != wantRelayed {
							t.Errorf("Gates(%+v)[ISSUE_TRACKER_RELAYED] = %v, want %v", env, g["ISSUE_TRACKER_RELAYED"], wantRelayed)
						}
						if g["ISSUE_TRACKER_RELAYED_LOG"] != (wantRelayed && carrier == "log") ||
							g["ISSUE_TRACKER_RELAYED_SOCKET"] != (wantRelayed && carrier == "socket") {
							t.Errorf("Gates(%+v) ISSUE_TRACKER_RELAYED _LOG/_SOCKET = %v/%v for relayed=%v",
								env, g["ISSUE_TRACKER_RELAYED_LOG"], g["ISSUE_TRACKER_RELAYED_SOCKET"], wantRelayed)
						}
					}
				}
			}
		}
	}
}
