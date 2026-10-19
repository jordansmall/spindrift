package promptassembly

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatchkind"
)

// trackerGates computes the Issue-Tracker gate family: the tracker
// read/write/filer descriptor gates and the PR-body ticket-reference gates.
// e.FilerEnabled is a plain Env field that nix resolves (issue #2533).
func trackerGates(e Env) map[string]bool {
	g := map[string]bool{}

	// nix resolves the three axis names at eval time (issue #2533). They are
	// dispatch-time-only forwards with no baked preamble default, so a host
	// launcher predating #2533 leaves itRead empty; nothing else ever does.
	// Fall back to e.IssueTracker's own arm, not to github, which would
	// contradict itself for a local or forgejo tracker.
	itRead, itWrite, itFiler := e.TrackerAxisRead, e.TrackerAxisWrite, e.TrackerAxisFiler
	if itRead == "" {
		itRead, itWrite, itFiler = issueTrackerAxisFallback(e.IssueTracker)
	}

	// ADR 0041 / issue #2593: an advise-only dispatch with the Filer provisioned
	// always uses the relay form, regardless of BOX_WRITE_ENABLED. So does a
	// research verdict on a tracker the Box cannot write (jira, issue #5245): it
	// rides the GITHUB axis for reads, but has no in-box write path. The butler
	// posts no verdict, so that term is scoped to ContractVerdict. Env forwards
	// IssueTracker as the empty string by default; resolve it to the default.
	trackerName := e.IssueTracker
	if trackerName == "" {
		trackerName = defaultIssueTracker
	}
	d := e.descriptor()
	researchForceRelay := (d.AdviseOnly && e.FilerEnabled) ||
		(verdictContract(d) && backend.ResearchVerdictRelayed(trackerName, e.FilerEnabled))

	// Exactly one of these three ever fires.
	g["ISSUE_TRACKER_GITHUB"] = itRead == "GITHUB"
	g["ISSUE_TRACKER_LOCAL"] = itRead == "LOCAL"
	g["ISSUE_TRACKER_FORGEJO"] = itRead == "FORGEJO"

	// A tracker with a direct write-step path (itWrite non-empty) forks on
	// BOX_WRITE_ENABLED; local (itWrite empty) renders neither pair, since its
	// write step always goes through the relay. These gates also drive the
	// research-verdict fragments, so researchForceRelay flips a would-be
	// _READWRITE case to its _READONLY sibling (ADR 0041, issue #2593).
	// A tracker with InBoxUnwritableTracker (jira) borrows the GITHUB write
	// axis but no write token reaches it, so its read-write arm is
	// ISSUE_TRACKER_UNWRITABLE_READWRITE, not _GITHUB_READWRITE: the work IF
	// BLOCKED comment relays through the host's note= field (issue #5263).
	row, _ := backend.ByName(trackerName)
	unwritable := row.InBoxUnwritableTracker
	// The backend-agnostic form of "the Box can read the tracker": gates a
	// slot whose shared fragment serves every reachable tracker.
	g["ISSUE_TRACKER_REACHABLE"] = !row.InBoxUnreachableTracker
	g["ISSUE_TRACKER_GITHUB_READWRITE"] = itWrite == "GITHUB" && e.BoxWriteEnabled && !researchForceRelay && !unwritable
	g["ISSUE_TRACKER_UNWRITABLE_READWRITE"] = unwritable && e.BoxWriteEnabled && !researchForceRelay
	g["ISSUE_TRACKER_GITHUB_READONLY"] = itWrite == "GITHUB" && (!e.BoxWriteEnabled || researchForceRelay)
	g["ISSUE_TRACKER_FORGEJO_READWRITE"] = itWrite == "FORGEJO" && e.BoxWriteEnabled && !researchForceRelay
	g["ISSUE_TRACKER_FORGEJO_READONLY"] = itWrite == "FORGEJO" && (!e.BoxWriteEnabled || researchForceRelay)

	// Backend-agnostic forms of the pairs above, for slot rows whose
	// per-folder fragments replace the backend-named gates: READWRITE is the
	// union of the two _READWRITE gates, RELAYED the union of every relayed
	// path (read-only, local, Filer-forced).
	g["ISSUE_TRACKER_READWRITE"] = itWrite != "" && e.BoxWriteEnabled && !researchForceRelay && !unwritable
	g["ISSUE_TRACKER_RELAYED"] = itWrite == "" || !e.BoxWriteEnabled || researchForceRelay

	// Selects the research OUTCOME's landing=none variant (issue #5130): the
	// union of the paths that render a relayed research-verdict fragment,
	// scoped to ContractVerdict because the butler is AdviseOnly too but
	// posts no verdict.
	g["RESEARCH_VERDICT_RELAYED"] = verdictContract(e.descriptor()) && g["ISSUE_TRACKER_RELAYED"]

	// On a work dispatch the relay activates on a read-only Box; a writable
	// Box keeps the direct gh/fj path, forked further on itFiler.
	// researchForceRelay (ADR 0041, #2593) activates the relay
	// unconditionally, so it has to be checked first.
	filerFileRelay := false
	filerFileDirectGH := false
	filerFileDirectForgejo := false
	if e.FilerEnabled {
		if researchForceRelay {
			filerFileRelay = true
		} else if !e.BoxWriteEnabled {
			filerFileRelay = true
		} else if itFiler == "FORGEJO" {
			filerFileDirectForgejo = true
		} else {
			filerFileDirectGH = true
		}
	}
	g["FILER_FILE_RELAY"] = filerFileRelay
	// The relay mechanism is kind-agnostic, but the label prose it selects
	// isn't (filer-label-relay*.md), so each kind gets its own gate, named by
	// the descriptor's FilerRelayGate.
	for _, kd := range dispatchkind.All {
		g[kd.FilerRelayGate] = false
	}
	g[e.descriptor().FilerRelayGate] = filerFileRelay
	g["FILER_FILE_DIRECT_GH"] = filerFileDirectGH
	g["FILER_FILE_DIRECT_FORGEJO"] = filerFileDirectForgejo

	// file-issues-direct.md renders whenever either direct fork is on.
	g["FILER_FILE_DIRECT_ANY"] = filerFileDirectGH || filerFileDirectForgejo

	// Exactly one of these is ever on, picked from ISSUE_TRACKER crossed with
	// LOCAL_ISSUE_REFERENCE. jira falls into the same else branch as github.
	prBodyLocalRef := false
	prBodyLocalNoref := false
	prBodyCloses := false
	if row.SlugIssueRefTracker {
		if e.LocalIssueReference {
			prBodyLocalRef = true
		} else {
			prBodyLocalNoref = true
		}
	} else {
		prBodyCloses = true
	}
	g["PR_BODY_CLOSES"] = prBodyCloses
	g["PR_BODY_LOCAL_REF"] = prBodyLocalRef
	g["PR_BODY_LOCAL_NOREF"] = prBodyLocalNoref

	// The slot rows key on the opt-in knob alone: which tracker has a file
	// for each is the folder's business, not a gate's.
	g["PR_BODY_ISSUE_REF"] = e.LocalIssueReference
	g["PR_BODY_NO_ISSUE_REF"] = !e.LocalIssueReference

	return g
}

// issueTrackerAxisFallback maps a raw IssueTracker value onto its three axis
// suffixes, the same mapping nix now performs at eval time. Used only by
// trackerGates's version-skew fallback; jira shares github's arm.
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
