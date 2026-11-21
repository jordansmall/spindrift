package promptassembly

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatchkind"
)

// trackerGates computes the Issue-Tracker gate family: the tracker
// read/write/filer descriptor gates, the PR-body ticket-reference gates, and
// the issue-route gate (ADR 0075).
// e.FilerEnabled is a plain Env field that nix resolves (issue #2533).
func trackerGates(e Env) map[string]bool {
	g := map[string]bool{}

	// Only a host launcher predating #2533 leaves itRead empty. Fall back to
	// e.IssueTracker's own arm, not to github, which would contradict itself
	// for a local or forgejo tracker.
	itRead, itWrite := e.TrackerAxisRead, e.TrackerAxisWrite
	if itRead == "" {
		itRead, itWrite, _ = issueTrackerAxisFallback(e.IssueTracker)
	}

	trackerName := e.IssueTracker
	if trackerName == "" {
		trackerName = defaultIssueTracker
	}

	// A routed tracker scope feeds many Target repos (ADR 0075); the fragment
	// names REPO_SLUG (guaranteed by launcherchecks.repoSlugRequired).
	g["ISSUE_ROUTED"] = e.IssueRouted

	// At most one of these three fires; jira (itRead "JIRA") fires none, as no
	// fragment row is keyed on a jira read gate.
	g["ISSUE_TRACKER_GITHUB"] = itRead == "GITHUB"
	g["ISSUE_TRACKER_LOCAL"] = itRead == "LOCAL"
	g["ISSUE_TRACKER_FORGEJO"] = itRead == "FORGEJO"

	// A tracker with InBoxUnwritableTracker (jira) borrows the GITHUB write
	// axis but no write token reaches it (issue #5263). Local (itWrite empty)
	// renders neither write-axis gate.
	row, _ := backend.ByName(trackerName)
	unwritable := row.InBoxUnwritableTracker
	// The backend-agnostic form of "the Box can read the tracker": gates a
	// slot whose shared fragment serves every reachable tracker.
	g["ISSUE_TRACKER_REACHABLE"] = !row.InBoxUnreachableTracker
	// Whether the Signal socket's issue read answers is the host's call: it
	// forwards BOX_SIGNAL_ISSUE_READ when it built a reader for this dispatch
	// (issues #6894, #6924). Local has no host reader and keeps its folder
	// fragment, its linked issues being injected host-side (ADR 0050).
	verb := e.SignalIssueRead
	g["ISSUE_TRACKER_LINKS_REACHABLE"] = verb
	g["ISSUE_TRACKER_LINKS_READABLE"] = row.InBoxUnreachableTracker || verb
	g["ISSUE_TRACKER_LINKS_UNREADABLE"] = !g["ISSUE_TRACKER_LINKS_READABLE"]
	// The deprecated per-backend issue-read rows must render verb wording only
	// where the verb exists, so an override that still names them matches the
	// shipped slot. jira keeps the GITHUB pair: an override that named them
	// before jira had its own read axis still renders the same text.
	githubIssueRead := itRead == "GITHUB" || itRead == "JIRA"
	g["ISSUE_TRACKER_GITHUB_LINKS_READABLE"] = githubIssueRead && verb
	g["ISSUE_TRACKER_GITHUB_LINKS_UNREADABLE"] = githubIssueRead && !verb
	g["ISSUE_TRACKER_FORGEJO_LINKS_READABLE"] = itRead == "FORGEJO" && verb
	g["ISSUE_TRACKER_FORGEJO_LINKS_UNREADABLE"] = itRead == "FORGEJO" && !verb
	// Likewise the issue-search verb follows BOX_SIGNAL_ISSUE_SEARCH, set when
	// the host has a searcher (issues #6895, #6924); local keeps its own folder
	// search. The Filer's dedup step forks on whether a search reaches the Box.
	searchVerb := e.SignalIssueSearch
	g["ISSUE_TRACKER_SEARCHABLE"] = searchVerb || itRead == "LOCAL"
	g["ISSUE_TRACKER_UNSEARCHABLE"] = !g["ISSUE_TRACKER_SEARCHABLE"]
	// The deprecated per-backend dedup rows, as for the issue read above.
	g["ISSUE_TRACKER_GITHUB_SEARCHABLE"] = itRead == "GITHUB" && searchVerb
	g["ISSUE_TRACKER_GITHUB_UNSEARCHABLE"] = itRead == "GITHUB" && !searchVerb
	g["ISSUE_TRACKER_FORGEJO_SEARCHABLE"] = itRead == "FORGEJO" && searchVerb
	g["ISSUE_TRACKER_FORGEJO_UNSEARCHABLE"] = itRead == "FORGEJO" && !searchVerb
	// InBoxUnwritableTracker (not local, which also lacks an in-Box client but
	// takes the writable arm) picks the FILER_RELAY_REASON fragment
	// (issue #6354).
	g["ISSUE_TRACKER_WRITABLE"] = !unwritable
	g["ISSUE_TRACKER_UNWRITABLE"] = unwritable
	g["ISSUE_TRACKER_GITHUB_READONLY"] = itWrite == "GITHUB"
	g["ISSUE_TRACKER_FORGEJO_READONLY"] = itWrite == "FORGEJO"

	// Selects the research OUTCOME's landing=none variant (issue #5130): scoped
	// to ContractVerdict because the butler is AdviseOnly too but posts no
	// verdict.
	g["RESEARCH_VERDICT_RELAYED"] = verdictContract(e.descriptor())

	// The Filer never files directly: with it on the roster, every filing goes
	// through the host relay (ADR 0041).
	filerFileRelay := e.FilerEnabled
	g["FILER_FILE_RELAY"] = filerFileRelay
	// The relay mechanism is kind-agnostic, but the label prose it selects
	// isn't (filer-label-relay*.md), so each kind gets its own gate, named by
	// the descriptor's FilerRelayGate.
	for _, kd := range dispatchkind.All {
		g[kd.FilerRelayGate] = false
	}
	g[e.descriptor().FilerRelayGate] = filerFileRelay

	// At most one of these is ever on, picked from ISSUE_TRACKER x CODE_FORGE x
	// LOCAL_ISSUE_REFERENCE. Closes is on only when the forge shares the
	// tracker's issue namespace.
	forgeRow, _ := backend.ByName(e.CodeForge)
	if e.CodeForge == "" {
		forgeRow = backend.GitHub
	}
	prBodyLocalRef := false
	prBodyLocalNoref := false
	if row.SlugIssueRefTracker {
		if e.LocalIssueReference {
			prBodyLocalRef = true
		} else {
			prBodyLocalNoref = true
		}
	}
	g["PR_BODY_CLOSES"] = backend.ClosesRef(forgeRow, row)
	g["PR_BODY_LOCAL_REF"] = prBodyLocalRef
	g["PR_BODY_LOCAL_NOREF"] = prBodyLocalNoref

	// The slot rows key on the opt-in knob alone: which tracker has a file
	// for each is the folder's business, not a gate's.
	g["PR_BODY_ISSUE_REF"] = e.LocalIssueReference
	g["PR_BODY_NO_ISSUE_REF"] = !e.LocalIssueReference

	return g
}

// issueTrackerAxisFallback maps a raw IssueTracker value onto its three axis
// suffixes, as nix does at eval time, for the version-skew fallback.
func issueTrackerAxisFallback(issueTracker string) (itRead, itWrite, itFiler string) {
	tracker := issueTracker
	if tracker == "" {
		tracker = defaultIssueTracker
	}
	return backend.TrackerAxisSignals(tracker)
}

// verdictContract reports whether d posts a research verdict comment. The
// butler is AdviseOnly too but posts none, so callers scope to this rather
// than AdviseOnly.
func verdictContract(d *dispatchkind.Descriptor) bool {
	switch d.Contract {
	case dispatchkind.ContractVerdict:
		return true
	}
	return false
}
