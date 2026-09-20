package promptassembly

// trackerGates computes the Issue-Tracker gate family: the tracker
// read/write/filer descriptor gates and the PR-body ticket-reference gates.
// Gates passes orchestratorEnabled in so it stays the one place deriving it;
// e.FilerEnabled is a plain Env field that nix resolves (issue #2533).
func trackerGates(e Env, orchestratorEnabled bool) map[string]bool {
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

	// ADR 0041 / issue #2593: a research dispatch with the Filer provisioned
	// always uses the relay form, with no ORCHESTRATOR_ENABLED condition and
	// regardless of BOX_WRITE_ENABLED. Env forwards DispatchKind as the empty
	// string by default, so the comparison has to resolve that default the
	// same way every other reader of the field does.
	kind := e.kind()
	researchForceRelay := kind == "research" && e.FilerEnabled

	// Exactly one of these three ever fires.
	g["ISSUE_TRACKER_GITHUB"] = itRead == "GITHUB"
	g["ISSUE_TRACKER_LOCAL"] = itRead == "LOCAL"
	g["ISSUE_TRACKER_FORGEJO"] = itRead == "FORGEJO"

	// A tracker with a direct write-step path (itWrite non-empty) forks on
	// BOX_WRITE_ENABLED; local (itWrite empty) renders neither pair, since its
	// write step always goes through the relay. These gates also drive the
	// research-verdict fragments, so researchForceRelay flips a would-be
	// _READWRITE case to its _READONLY sibling (ADR 0041, issue #2593).
	g["ISSUE_TRACKER_GITHUB_READWRITE"] = itWrite == "GITHUB" && e.BoxWriteEnabled && !researchForceRelay
	g["ISSUE_TRACKER_GITHUB_READONLY"] = itWrite == "GITHUB" && (!e.BoxWriteEnabled || researchForceRelay)
	g["ISSUE_TRACKER_FORGEJO_READWRITE"] = itWrite == "FORGEJO" && e.BoxWriteEnabled && !researchForceRelay
	g["ISSUE_TRACKER_FORGEJO_READONLY"] = itWrite == "FORGEJO" && (!e.BoxWriteEnabled || researchForceRelay)

	// On a work dispatch the relay activates only on read-only plus the
	// orchestrator gate; every other combination keeps the direct gh/fj path,
	// forked further on itFiler. researchForceRelay (ADR 0041, #2593)
	// activates the relay unconditionally, so it has to be checked first.
	filerFileRelay := false
	filerFileDirectGH := false
	filerFileDirectForgejo := false
	if e.FilerEnabled {
		if researchForceRelay {
			filerFileRelay = true
		} else if !e.BoxWriteEnabled && orchestratorEnabled {
			filerFileRelay = true
		} else if itFiler == "FORGEJO" {
			filerFileDirectForgejo = true
		} else {
			filerFileDirectGH = true
		}
	}
	g["FILER_FILE_RELAY"] = filerFileRelay
	// FILER_FILE_RELAY stays kind-agnostic because the relay mechanism is the
	// same for work and research. The label the launcher applies host-side is
	// not: agent-review-finding for work, agent-research-finding for research,
	// named in filer-label-relay.md. These two split that same boolean by kind
	// (issue #2593).
	g["FILER_FILE_RELAY_RESEARCH"] = researchForceRelay
	g["FILER_FILE_RELAY_WORK"] = filerFileRelay && !researchForceRelay
	g["FILER_FILE_DIRECT_GH"] = filerFileDirectGH
	g["FILER_FILE_DIRECT_FORGEJO"] = filerFileDirectForgejo

	// file-issues-direct.md renders whenever either direct fork is on.
	g["FILER_FILE_DIRECT_ANY"] = filerFileDirectGH || filerFileDirectForgejo

	// Exactly one of these is ever on, picked from ISSUE_TRACKER crossed with
	// LOCAL_ISSUE_REFERENCE. jira falls into the same else branch as github.
	tracker := e.IssueTracker
	if tracker == "" {
		tracker = defaultIssueTracker
	}
	prBodyLocalRef := false
	prBodyLocalNoref := false
	prBodyCloses := false
	if tracker == "local" {
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
	switch tracker {
	case "local":
		return "LOCAL", "", "GH"
	case "forgejo":
		return "FORGEJO", "FORGEJO", "FORGEJO"
	default:
		return "GITHUB", "GITHUB", "GH"
	}
}
