package promptassembly

// trackerGates computes the Issue-Tracker gate family: the tracker
// read/write/filer descriptor gates and the PR-body ticket-reference
// gates (entrypoint.sh: 801-814, 816-860, 862-938). orchestratorEnabled is
// Gates's own ORCHESTRATOR value, passed in rather than re-derived here, so
// Gates stays the one place that resolves it; e.FilerEnabled is read
// directly since it's already a plain Env field (nix's precomputed roster
// fact, issue #2533), not something Gates itself derives.
func trackerGates(e Env, orchestratorEnabled bool) map[string]bool {
	g := map[string]bool{}

	// ISSUE_TRACKER -> per-axis descriptor (entrypoint.sh: 801-814): itRead
	// is the issue-read step suffix (always one of GITHUB/LOCAL/FORGEJO).
	// jira shares github's arm since it rides the same in-box reachability.
	// nix already resolves entrypoint.sh's "${ISSUE_TRACKER:-github}" case
	// statement once, at eval time, so the three axis names arrive
	// pre-resolved on Env (TrackerAxisRead/TrackerAxisWrite/
	// TrackerAxisFiler) rather than being re-derived here (issue #2533).
	//
	// BOX_TRACKER_AXIS_READ/WRITE/FILER are dispatch-time-only forwards
	// with no baked preamble default (unlike e.g. AGENTS_JSON_TEMPLATE), so
	// an older host launcher binary that predates issue #2533 -- and
	// therefore never sets these env vars at all -- dispatching against a
	// newer box image leaves TrackerAxisRead empty here even though this
	// package is fully wired up to expect it. TrackerAxisRead is never
	// legitimately empty for a resolved axis (only TrackerAxisWrite can be,
	// for the "local" tracker), so an empty itRead is unambiguously that
	// version-skew case, not a legitimate axis value. Falling open here
	// reproduces entrypoint.sh's old bash "${ISSUE_TRACKER:-github}" case
	// statement, re-derived from e.IssueTracker itself -- still forwarded
	// on Env for exactly this fallback (env.go: 93-101) -- as a
	// version-skew safety net, rather than hardcoding the github/jira arm
	// regardless of what e.IssueTracker actually says: a version-skewed
	// local or forgejo tracker would otherwise render a self-contradictory
	// prompt (e.g. ISSUE_TRACKER_GITHUB alongside PR_BODY_LOCAL_NOREF)
	// instead of falling back to its own correct arm.
	itRead, itWrite, itFiler := e.TrackerAxisRead, e.TrackerAxisWrite, e.TrackerAxisFiler
	if itRead == "" {
		itRead, itWrite, itFiler = issueTrackerAxisFallback(e.IssueTracker)
	}

	// researchForceRelay is the ADR 0041 / issue #2593 research special-case:
	// a research dispatch with the Filer provisioned always uses the relay
	// form for both the verdict-comment write step and the filer's own
	// write-mechanism -- unconditionally, with no ORCHESTRATOR_ENABLED
	// condition, and regardless of BOX_WRITE_ENABLED (read-write or
	// read-only). DispatchKind is fixed for the whole Assemble() call, so
	// this can only ever be true while rendering research-path fragments;
	// it never touches the work-path issue-blocked-comment or
	// file-issues-direct fragments below. kind defaults to "work" the same
	// way assemble.go/validate.go do, since DispatchKind is forwarded
	// empty-string on Env for the default case rather than "work" itself.
	kind := e.DispatchKind
	if kind == "" {
		kind = defaultDispatchKind
	}
	researchForceRelay := kind == "research" && e.FilerEnabled

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
	// through the relay form instead. This same pair of gates also drives
	// the research-path research-verdict-github(-readonly).md fragments, so
	// researchForceRelay (ADR 0041 / issue #2593) flips a would-be
	// _READWRITE case to its _READONLY sibling whenever the current
	// dispatch is research with the Filer provisioned; it's always false
	// for a work dispatch (or a Filer-less research dispatch), leaving
	// these expressions byte-for-byte their pre-#2593 shape in every case
	// this function's other callers ever actually observe.
	g["ISSUE_TRACKER_GITHUB_READWRITE"] = itWrite == "GITHUB" && e.BoxWriteEnabled && !researchForceRelay
	g["ISSUE_TRACKER_GITHUB_READONLY"] = itWrite == "GITHUB" && (!e.BoxWriteEnabled || researchForceRelay)
	g["ISSUE_TRACKER_FORGEJO_READWRITE"] = itWrite == "FORGEJO" && e.BoxWriteEnabled && !researchForceRelay
	g["ISSUE_TRACKER_FORGEJO_READONLY"] = itWrite == "FORGEJO" && (!e.BoxWriteEnabled || researchForceRelay)

	// The filer's write-mechanism gates (entrypoint.sh: 816-860): on a work
	// dispatch, relay only activates on read-only (BOX_WRITE_ENABLED
	// absent) + the orchestrator gate; every other combination keeps the
	// direct gh/fj path, forked further on itFiler. On a research dispatch
	// (researchForceRelay, ADR 0041 / issue #2593), relay activates
	// unconditionally instead -- no orchestrator condition, and regardless
	// of BOX_WRITE_ENABLED (applies in read-write mode too) -- so the
	// research special-case is checked first, ahead of the work-path
	// !e.BoxWriteEnabled && orchestratorEnabled check below. Both stay off
	// when the filer isn't configured at all.
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
	// FILER_FILE_RELAY_RESEARCH/FILER_FILE_RELAY_WORK (issue #2593 review
	// finding): the write-mechanism itself (host-mediated
	// SPINDRIFT_ISSUE_INTENT relay vs. direct gh/fj) is identical between
	// work and research relay, which is why FILER_FILE_RELAY above stays
	// kind-agnostic and keeps driving every other relay-gated row
	// (file-issues-relay.md, filer-file-relay.md) unchanged. But the label
	// the launcher applies host-side once it files each relayed issue
	// differs by kind -- agent-review-finding for work (settle/gate.go),
	// agent-research-finding for research (settle/research.go:97) -- and
	// filer-label-relay.md's prose names that label explicitly. These two
	// gates are the kind-split view of that same filerFileRelay boolean,
	// mutually exclusive and together exactly equal to it (researchForceRelay
	// implies filerFileRelay), so filer-label-relay.md can be split into a
	// work-worded and a research-worded fragment without touching the
	// combined gate any other row still relies on.
	g["FILER_FILE_RELAY_RESEARCH"] = researchForceRelay
	g["FILER_FILE_RELAY_WORK"] = filerFileRelay && !researchForceRelay
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

// issueTrackerAxisFallback reproduces entrypoint.sh's old
// "${ISSUE_TRACKER:-github}" case statement (801-814), mapping the raw
// IssueTracker value onto its three gate-family suffixes -- the same
// mapping nix now performs at eval time for TrackerAxisRead/Write/Filer.
// Used only by trackerGates's version-skew fallback above, when an older
// host launcher never forwarded the nix-resolved axis at all; jira shares
// github's arm since it rides the same in-box reachability.
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
