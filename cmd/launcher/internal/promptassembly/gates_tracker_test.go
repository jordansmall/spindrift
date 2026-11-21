package promptassembly

import (
	"testing"

	"spindrift.dev/launcher/internal/backend"
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
			name:            "jira",
			trackerAxisRead: "JIRA",
			want: map[string]bool{
				"ISSUE_TRACKER_GITHUB":  false,
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
// research-verdict write-axis gates: github and forgejo each name their axis,
// local has no direct write path at all, and jira borrows github's.
// Every case sets a non-empty TrackerAxisRead so the itRead=="" version-skew
// fallback (issue #2533), covered in TestGatesIssueTrackerReadAxis, stays off.
func TestGatesIssueTrackerWriteAxis(t *testing.T) {
	cases := []struct {
		name             string
		trackerAxisRead  string
		trackerAxisWrite string
		want             map[string]bool
	}{
		{"github", "GITHUB", "GITHUB", map[string]bool{
			"ISSUE_TRACKER_GITHUB_READONLY": true, "ISSUE_TRACKER_FORGEJO_READONLY": false,
		}},
		{"forgejo", "FORGEJO", "FORGEJO", map[string]bool{
			"ISSUE_TRACKER_GITHUB_READONLY": false, "ISSUE_TRACKER_FORGEJO_READONLY": true,
		}},
		{"local has no direct write-step path", "LOCAL", "", map[string]bool{
			"ISSUE_TRACKER_GITHUB_READONLY": false, "ISSUE_TRACKER_FORGEJO_READONLY": false,
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{TrackerAxisRead: tc.trackerAxisRead, TrackerAxisWrite: tc.trackerAxisWrite})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(TrackerAxisRead=%q, TrackerAxisWrite=%q)[%q] = %v, want %v", tc.trackerAxisRead, tc.trackerAxisWrite, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesFilerRelay covers the filer's write-mechanism gates: the Filer
// always files through the host relay (ADR 0041), so the relay gates follow
// FilerEnabled alone, and each dispatch kind lights only its own label gate.
func TestGatesFilerRelay(t *testing.T) {
	cases := []struct {
		name         string
		kind         string
		filerEnabled bool
		want         map[string]bool
	}{
		{"filer not configured: everything off", "work", false, map[string]bool{
			"FILER_FILE_RELAY": false, "FILER_FILE_RELAY_WORK": false,
			"FILER_FILE_RELAY_RESEARCH": false, "FILER_FILE_RELAY_BUTLER": false,
		}},
		{"work", "work", true, map[string]bool{
			"FILER_FILE_RELAY": true, "FILER_FILE_RELAY_WORK": true,
			"FILER_FILE_RELAY_RESEARCH": false, "FILER_FILE_RELAY_BUTLER": false,
		}},
		{"research", "research", true, map[string]bool{
			"FILER_FILE_RELAY": true, "FILER_FILE_RELAY_WORK": false,
			"FILER_FILE_RELAY_RESEARCH": true, "FILER_FILE_RELAY_BUTLER": false,
		}},
		{"butler", "butler", true, map[string]bool{
			"FILER_FILE_RELAY": true, "FILER_FILE_RELAY_WORK": false,
			"FILER_FILE_RELAY_RESEARCH": false, "FILER_FILE_RELAY_BUTLER": true,
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := Env{DispatchKind: tc.kind, FilerEnabled: tc.filerEnabled, TrackerAxisRead: "GITHUB", TrackerAxisFiler: "GH"}
			got := Gates(env)
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(%+v)[%q] = %v, want %v", env, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesPRBodyReference covers the PR-body ticket-reference gates: at most
// one of PR_BODY_CLOSES/PR_BODY_LOCAL_REF/PR_BODY_LOCAL_NOREF is ever on,
// picked from ISSUE_TRACKER x CODE_FORGE x LOCAL_ISSUE_REFERENCE. PR_BODY_CLOSES
// is on only when the forge shares the tracker's issue namespace; an unset
// CODE_FORGE reads as github.
func TestGatesPRBodyReference(t *testing.T) {
	closes := map[string]bool{"PR_BODY_CLOSES": true, "PR_BODY_LOCAL_REF": false, "PR_BODY_LOCAL_NOREF": false}
	none := map[string]bool{"PR_BODY_CLOSES": false, "PR_BODY_LOCAL_REF": false, "PR_BODY_LOCAL_NOREF": false}
	localRef := map[string]bool{"PR_BODY_CLOSES": false, "PR_BODY_LOCAL_REF": true, "PR_BODY_LOCAL_NOREF": false}
	localNoref := map[string]bool{"PR_BODY_CLOSES": false, "PR_BODY_LOCAL_REF": false, "PR_BODY_LOCAL_NOREF": true}
	cases := []struct {
		name                string
		issueTracker        string
		codeForge           string
		localIssueReference bool
		want                map[string]bool
	}{
		{"github tracker on an unset forge closes", "github", "", false, closes},
		{"github tracker on a github forge closes", "github", "github", false, closes},
		{"github tracker ignores LOCAL_ISSUE_REFERENCE", "github", "github", true, closes},
		{"forgejo tracker on a forgejo forge closes", "forgejo", "forgejo", false, closes},
		{"forgejo tracker on a github forge gets no Closes step", "forgejo", "github", false, none},
		{"github tracker on a forgejo forge gets no Closes step", "github", "forgejo", false, none},
		{"jira tracker on a github forge gets no Closes step", "jira", "github", false, none},
		{"jira tracker on a forgejo forge gets no Closes step", "jira", "forgejo", false, none},
		{"github tracker on an unknown forge gets no Closes step", "github", "nosuchforge", false, none},
		{"local default: no reference", "local", "", false, localNoref},
		{"local opt-in: breadcrumb reference", "local", "", true, localRef},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{IssueTracker: tc.issueTracker, CodeForge: tc.codeForge, LocalIssueReference: tc.localIssueReference})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(IssueTracker=%q, CodeForge=%q, LocalIssueReference=%v)[%q] = %v, want %v",
						tc.issueTracker, tc.codeForge, tc.localIssueReference, k, got[k], want)
				}
			}
		})
	}
}

// TestGatesPRBodyClosesPairs walks every tracker x forge row pair against a
// hand-written set of the pairs that close, so a new row, or a change that
// splits the prompt from the host's intent, fails here rather than silently
// shipping or dropping a Closes step.
func TestGatesPRBodyClosesPairs(t *testing.T) {
	closing := map[[2]string]bool{
		{"github", "github"}:   true,
		{"forgejo", "forgejo"}: true,
	}
	for _, tr := range backend.Registry {
		if !tr.ValidAsTracker {
			continue
		}
		for _, fg := range backend.Registry {
			if !fg.ValidAsCodeForge {
				continue
			}
			for _, localRef := range []bool{false, true} {
				got := Gates(Env{IssueTracker: tr.Name, CodeForge: fg.Name, LocalIssueReference: localRef})
				if want := closing[[2]string{tr.Name, fg.Name}]; got["PR_BODY_CLOSES"] != want {
					t.Errorf("tracker %q on forge %q (LocalIssueReference=%v): PR_BODY_CLOSES = %v, want %v",
						tr.Name, fg.Name, localRef, got["PR_BODY_CLOSES"], want)
				}
				on := 0
				for _, k := range []string{"PR_BODY_CLOSES", "PR_BODY_LOCAL_REF", "PR_BODY_LOCAL_NOREF"} {
					if got[k] {
						on++
					}
				}
				if on > 1 {
					t.Errorf("tracker %q on forge %q (LocalIssueReference=%v): %d PR-body reference gates on, want at most 1",
						tr.Name, fg.Name, localRef, on)
				}
			}
		}
	}
}

// TestGatesResearchVerdictRelayed pins that RESEARCH_VERDICT_RELAYED is on for
// every research dispatch, whatever the tracker or Filer, and off for the
// kinds that post no verdict (ContractVerdict only; issue #5130).
func TestGatesResearchVerdictRelayed(t *testing.T) {
	cases := []struct {
		name         string
		kind         string
		filerEnabled bool
		read, write  string
		want         bool
	}{
		{"github", "research", false, "GITHUB", "GITHUB", true},
		{"github with the Filer", "research", true, "GITHUB", "GITHUB", true},
		{"forgejo", "research", false, "FORGEJO", "FORGEJO", true},
		{"local", "research", false, "LOCAL", "", true},
		{"work", "work", false, "GITHUB", "GITHUB", false},
		{"work local", "work", false, "LOCAL", "", false},
		{"butler", "butler", false, "GITHUB", "GITHUB", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := Env{
				DispatchKind:     tc.kind,
				FilerEnabled:     tc.filerEnabled,
				TrackerAxisRead:  tc.read,
				TrackerAxisWrite: tc.write,
			}
			if got := Gates(env)["RESEARCH_VERDICT_RELAYED"]; got != tc.want {
				t.Errorf("Gates(%+v)[RESEARCH_VERDICT_RELAYED] = %v, want %v", env, got, tc.want)
			}
		})
	}
}

// TestGatesJiraRelays pins that a jira dispatch relays in every kind (issue
// #5245): jira stays on the GITHUB write axis for the blocked-comment relay,
// reads through its own axis, and the Filer files through the relay (issue
// #6354).
func TestGatesJiraRelays(t *testing.T) {
	cases := []struct {
		name        string
		kind        string
		filer       bool
		wantRelayed bool
	}{
		{"research, Filer off", "research", false, true},
		{"research, Filer on", "research", true, true},
		{"work, Filer off", "work", false, false},
		{"work, Filer on", "work", true, false},
		{"butler, Filer off", "butler", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := Gates(Env{DispatchKind: tc.kind, IssueTracker: "jira", FilerEnabled: tc.filer})
			if g["ISSUE_TRACKER_GITHUB"] {
				t.Error("ISSUE_TRACKER_GITHUB = true, want false (jira reads through its own axis)")
			}
			if !g["ISSUE_TRACKER_GITHUB_READONLY"] {
				t.Error("ISSUE_TRACKER_GITHUB_READONLY = false, want true (jira borrows the GITHUB write axis)")
			}
			if g["RESEARCH_VERDICT_RELAYED"] != tc.wantRelayed {
				t.Errorf("RESEARCH_VERDICT_RELAYED = %v, want %v", g["RESEARCH_VERDICT_RELAYED"], tc.wantRelayed)
			}
			if g["FILER_FILE_RELAY"] != tc.filer {
				t.Errorf("FILER_FILE_RELAY = %v, want %v", g["FILER_FILE_RELAY"], tc.filer)
			}
		})
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

// TestGatesIssueTrackerLinksReadable pins the gates that select the
// issue-read wording: they follow the host's BOX_SIGNAL_ISSUE_READ signal
// alone, not the carrier or the backend row; local keeps its own folder
// fragment with or without it (issues #6894, #6924).
func TestGatesIssueTrackerLinksReadable(t *testing.T) {
	for _, tc := range []struct {
		name          string
		tracker, read string
		carrier       string
		signal        bool // SignalIssueRead
		readable      bool
	}{
		{"unset tracker, signal", "", "GITHUB", "socket", true, true},
		{"github socket, signal", "github", "GITHUB", "socket", true, true},
		{"forgejo socket, signal", "forgejo", "FORGEJO", "socket", true, true},
		{"jira socket, signal", "jira", "JIRA", "socket", true, true},
		{"github socket, no signal", "github", "GITHUB", "socket", false, false},
		{"jira socket, no signal", "jira", "JIRA", "socket", false, false},
		{"local socket, no signal", "local", "LOCAL", "socket", false, true},
		{"github log, no signal", "github", "GITHUB", "log", false, false},
		{"forgejo log, no signal", "forgejo", "FORGEJO", "log", false, false},
		{"local log, no signal", "local", "LOCAL", "log", false, true},
		{"github empty carrier, no signal", "github", "GITHUB", "", false, false},
		{"github log, signal", "github", "GITHUB", "log", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := Gates(Env{IssueTracker: tc.tracker, TrackerAxisRead: tc.read, SignalCarrier: tc.carrier, SignalIssueRead: tc.signal})
			for gate, want := range map[string]bool{
				"ISSUE_TRACKER_LINKS_READABLE":           tc.readable,
				"ISSUE_TRACKER_LINKS_UNREADABLE":         !tc.readable,
				"ISSUE_TRACKER_LINKS_REACHABLE":          tc.signal,
				"ISSUE_TRACKER_GITHUB_LINKS_READABLE":    (tc.read == "GITHUB" || tc.read == "JIRA") && tc.signal,
				"ISSUE_TRACKER_GITHUB_LINKS_UNREADABLE":  (tc.read == "GITHUB" || tc.read == "JIRA") && !tc.signal,
				"ISSUE_TRACKER_FORGEJO_LINKS_READABLE":   tc.read == "FORGEJO" && tc.signal,
				"ISSUE_TRACKER_FORGEJO_LINKS_UNREADABLE": tc.read == "FORGEJO" && !tc.signal,
			} {
				if got := g[gate]; got != want {
					t.Errorf("[%s] = %v, want %v", gate, got, want)
				}
			}
		})
	}
}

// TestGatesIssueTrackerSearchable pins the gates that select the Filer's
// dedup step: a search reaches the Box through the host's
// BOX_SIGNAL_ISSUE_SEARCH signal (any tracker whose host searcher exists,
// jira included) or through local's own folder search; every other
// combination takes the no-search arm (issues #6895, #6924).
func TestGatesIssueTrackerSearchable(t *testing.T) {
	for _, tc := range []struct {
		name          string
		tracker, read string
		carrier       string
		signal        bool // SignalIssueSearch
		searchable    bool
	}{
		{"github socket, signal", "github", "GITHUB", "socket", true, true},
		{"forgejo socket, signal", "forgejo", "FORGEJO", "socket", true, true},
		{"jira socket, signal", "jira", "JIRA", "socket", true, true},
		{"github socket, no signal", "github", "GITHUB", "socket", false, false},
		{"jira socket, no signal", "jira", "JIRA", "socket", false, false},
		{"local socket, no signal", "local", "LOCAL", "socket", false, true},
		{"github log, no signal", "github", "GITHUB", "log", false, false},
		{"forgejo log, no signal", "forgejo", "FORGEJO", "log", false, false},
		{"local log, no signal", "local", "LOCAL", "log", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := Gates(Env{IssueTracker: tc.tracker, TrackerAxisRead: tc.read, SignalCarrier: tc.carrier, SignalIssueSearch: tc.signal})
			for gate, want := range map[string]bool{
				"ISSUE_TRACKER_SEARCHABLE":           tc.searchable,
				"ISSUE_TRACKER_UNSEARCHABLE":         !tc.searchable,
				"ISSUE_TRACKER_GITHUB_SEARCHABLE":    tc.read == "GITHUB" && tc.signal,
				"ISSUE_TRACKER_GITHUB_UNSEARCHABLE":  tc.read == "GITHUB" && !tc.signal,
				"ISSUE_TRACKER_FORGEJO_SEARCHABLE":   tc.read == "FORGEJO" && tc.signal,
				"ISSUE_TRACKER_FORGEJO_UNSEARCHABLE": tc.read == "FORGEJO" && !tc.signal,
			} {
				if got := g[gate]; got != want {
					t.Errorf("[%s] = %v, want %v", gate, got, want)
				}
			}
		})
	}
}

// TestGatesIssueTrackerWritable pins the inverse pair that selects the Filer's
// relay reason: a tracker with no in-Box client (jira) never claims a
// read-only token, however the Filer files (issue #6354).
func TestGatesIssueTrackerWritable(t *testing.T) {
	for _, tc := range []struct {
		tracker, read string
		writable      bool
	}{
		{"", "GITHUB", true},
		{"github", "GITHUB", true},
		{"forgejo", "FORGEJO", true},
		{"local", "LOCAL", true},
		{"jira", "JIRA", false},
	} {
		g := Gates(Env{IssueTracker: tc.tracker, TrackerAxisRead: tc.read})
		if got := g["ISSUE_TRACKER_WRITABLE"]; got != tc.writable {
			t.Errorf("Gates(IssueTracker=%q)[ISSUE_TRACKER_WRITABLE] = %v, want %v", tc.tracker, got, tc.writable)
		}
		if got := g["ISSUE_TRACKER_UNWRITABLE"]; got != !tc.writable {
			t.Errorf("Gates(IssueTracker=%q)[ISSUE_TRACKER_UNWRITABLE] = %v, want %v", tc.tracker, got, !tc.writable)
		}
	}
}

// TestGatesResearchVerdictRelayedCarrierSplit pins that the relayed gate
// splits into exactly one of _LOG/_SOCKET by Signal carrier, and that
// neither half is on for a kind that posts no verdict.
func TestGatesResearchVerdictRelayedCarrierSplit(t *testing.T) {
	cases := []struct {
		name            string
		kind            string
		carrier         string
		wantLog, wantSk bool
	}{
		{"log carrier", "research", "log", true, false},
		{"empty carrier means log", "research", "", true, false},
		{"socket carrier", "research", "socket", false, true},
		{"socket carrier, no verdict", "work", "socket", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := Gates(Env{
				DispatchKind:     tc.kind,
				SignalCarrier:    tc.carrier,
				TrackerAxisRead:  "GITHUB",
				TrackerAxisWrite: "GITHUB",
			})
			if g["RESEARCH_VERDICT_RELAYED_LOG"] != tc.wantLog || g["RESEARCH_VERDICT_RELAYED_SOCKET"] != tc.wantSk {
				t.Errorf("_LOG/_SOCKET = %v/%v, want %v/%v", g["RESEARCH_VERDICT_RELAYED_LOG"], g["RESEARCH_VERDICT_RELAYED_SOCKET"], tc.wantLog, tc.wantSk)
			}
		})
	}
}

// TestGatesPRBodyIssueRefKeysOnKnob pins the PR-body ticket-reference gates to
// LOCAL_ISSUE_REFERENCE alone, never a tracker name: the folder decides which
// tracker has a file for them, and exactly one of the pair is on.
func TestGatesPRBodyIssueRefKeysOnKnob(t *testing.T) {
	for _, tracker := range []string{"github", "forgejo", "local", "jira"} {
		for _, ref := range []bool{true, false} {
			g := Gates(Env{IssueTracker: tracker, LocalIssueReference: ref})
			if g["PR_BODY_ISSUE_REF"] != ref || g["PR_BODY_NO_ISSUE_REF"] == ref {
				t.Errorf("tracker %s ref=%v: PR_BODY_ISSUE_REF=%v PR_BODY_NO_ISSUE_REF=%v", tracker, ref, g["PR_BODY_ISSUE_REF"], g["PR_BODY_NO_ISSUE_REF"])
			}
		}
	}
}

// TestGatesPRBodyClosesMatchesBackend pins PR_BODY_CLOSES to
// backend.ClosesRef, the predicate settle's host Closes append reads, so prompt
// and host cannot drift.
func TestGatesPRBodyClosesMatchesBackend(t *testing.T) {
	for _, tr := range backend.Registry {
		if !tr.ValidAsTracker {
			continue
		}
		for _, fg := range backend.Registry {
			if !fg.ValidAsCodeForge {
				continue
			}
			got := Gates(Env{IssueTracker: tr.Name, CodeForge: fg.Name})["PR_BODY_CLOSES"]
			if want := backend.ClosesRef(fg, tr); got != want {
				t.Errorf("tracker %q on forge %q: PR_BODY_CLOSES = %v, want backend.ClosesRef = %v", tr.Name, fg.Name, got, want)
			}
		}
	}
	// An empty CODE_FORGE reads as github.
	if !Gates(Env{IssueTracker: "github"})["PR_BODY_CLOSES"] {
		t.Error("github tracker on an empty CODE_FORGE: PR_BODY_CLOSES = false, want true")
	}
}

// ISSUE_ROUTED follows the route (BOX_ISSUE_ROUTED) alone: the launcher
// requires REPO_SLUG whenever a route is set (ADR 0075).
func TestGatesIssueRouted(t *testing.T) {
	cases := []struct {
		name   string
		routed bool
		want   bool
	}{
		{name: "unrouted", want: false},
		{name: "routed", routed: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{IssueRouted: tc.routed})["ISSUE_ROUTED"]
			if got != tc.want {
				t.Errorf("Gates(IssueRouted=%v)[ISSUE_ROUTED] = %v, want %v", tc.routed, got, tc.want)
			}
		})
	}
}
