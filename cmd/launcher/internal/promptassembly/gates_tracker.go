package promptassembly

// trackerGates computes the Issue-Tracker gate family: the tracker
// read/write/filer descriptor gates and the PR-body ticket-reference
// gates (entrypoint.sh: 801-814, 816-860, 862-938). filerEnabled and
// orchestratorEnabled are Gates's own FILER_ENABLED/ORCHESTRATOR values,
// passed in rather than re-derived here, so AgentsJSONTemplate parsing
// stays in exactly one place (Gates itself).
func trackerGates(e Env, filerEnabled, orchestratorEnabled bool) map[string]bool {
	g := map[string]bool{}

	// ISSUE_TRACKER -> per-axis descriptor (entrypoint.sh: 801-814): itRead
	// is the issue-read step suffix (always one of GITHUB/LOCAL/FORGEJO).
	// jira shares github's arm since it rides the same in-box reachability.
	// One call binds all three axis names; each is read below by the gate
	// family it feeds.
	itRead, itWrite, itFiler := issueTrackerAxis(e.IssueTracker)

	// The issue-read step gate (entrypoint.sh: 891-904): exactly one of
	// ISSUE_TRACKER_GITHUB/ISSUE_TRACKER_LOCAL/ISSUE_TRACKER_FORGEJO fires,
	// selected by itRead above.
	g["ISSUE_TRACKER_GITHUB"] = itRead == "GITHUB"
	g["ISSUE_TRACKER_LOCAL"] = itRead == "LOCAL"
	g["ISSUE_TRACKER_FORGEJO"] = itRead == "FORGEJO"

	// The issue-blocked-comment/research-verdict write-step gates
	// (entrypoint.sh: 906-938): a tracker with a direct write-step path
	// (itWrite non-empty) forks on BOX_WRITE_ENABLED between the
	// _READWRITE and _READONLY arm; local (itWrite empty) renders neither
	// pair regardless of BOX_WRITE_ENABLED -- its write step always goes
	// through the relay form instead.
	g["ISSUE_TRACKER_GITHUB_READWRITE"] = itWrite == "GITHUB" && e.BoxWriteEnabled
	g["ISSUE_TRACKER_GITHUB_READONLY"] = itWrite == "GITHUB" && !e.BoxWriteEnabled
	g["ISSUE_TRACKER_FORGEJO_READWRITE"] = itWrite == "FORGEJO" && e.BoxWriteEnabled
	g["ISSUE_TRACKER_FORGEJO_READONLY"] = itWrite == "FORGEJO" && !e.BoxWriteEnabled

	// The filer's write-mechanism gates (entrypoint.sh: 816-860): relay
	// only activates on read-only (BOX_WRITE_ENABLED absent) + the
	// orchestrator gate; every other combination keeps the direct gh/fj
	// path, forked further on itFiler. Both stay off when the filer isn't
	// configured at all.
	filerFileRelay := false
	filerFileDirectGH := false
	filerFileDirectForgejo := false
	if filerEnabled {
		if !e.BoxWriteEnabled && orchestratorEnabled {
			filerFileRelay = true
		} else if itFiler == "FORGEJO" {
			filerFileDirectForgejo = true
		} else {
			filerFileDirectGH = true
		}
	}
	g["FILER_FILE_RELAY"] = filerFileRelay
	g["FILER_FILE_DIRECT_GH"] = filerFileDirectGH
	g["FILER_FILE_DIRECT_FORGEJO"] = filerFileDirectForgejo

	// file-issues-direct.md renders whenever either direct fork above is on
	// (entrypoint.sh: 851-860).
	g["FILER_FILE_DIRECT_ANY"] = filerFileDirectGH || filerFileDirectForgejo

	// The PR-body ticket-reference gates (entrypoint.sh: 862-889): exactly
	// one is ever on, picked from ISSUE_TRACKER x LOCAL_ISSUE_REFERENCE.
	// jira falls into the same else branch as github here.
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

// issueTrackerAxis reproduces entrypoint.sh's "${ISSUE_TRACKER:-github}"
// case statement (801-814), mapping the raw ISSUE_TRACKER value onto its
// three gate-family suffixes: itRead (issue-read step, always one of
// GITHUB/LOCAL/FORGEJO), itWrite (direct write-step suffix, empty when the
// tracker has no in-box direct-write path -- local always relays), and
// itFiler (filer direct-write suffix). jira shares github's in-box
// reachability, so it rides the GITHUB/GH arms alongside it.
func issueTrackerAxis(issueTracker string) (itRead, itWrite, itFiler string) {
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
