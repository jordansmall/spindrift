package promptassembly

// trackerGates computes the Issue-Tracker gate family: the tracker
// read/write/filer descriptor gates and the PR-body ticket-reference gates.
// orchestratorEnabled is Gates's own ORCHESTRATOR value, passed in rather than
// re-derived, so Gates stays the one place that resolves it; e.FilerEnabled is
// read directly since it is already a plain Env field.
func trackerGates(e Env, orchestratorEnabled bool) map[string]bool {
	g := map[string]bool{}

	// ISSUE_TRACKER -> per-axis descriptor. itRead is the issue-read step
	// suffix (always GITHUB/LOCAL/FORGEJO); jira shares github's arm since it
	// rides the same in-box reachability. nix resolves the three axis names at
	// eval time, so they arrive pre-resolved on Env.
	//
	// The BOX_TRACKER_AXIS_* forwards are dispatch-time-only, with no baked
	// preamble default, so an older host launcher that never sets them,
	// dispatching against a newer box image, leaves TrackerAxisRead empty here.
	// That is unambiguously version skew -- a resolved read axis is never
	// legitimately empty (only the write axis can be, for "local"). Fall back
	// by re-deriving from e.IssueTracker, still forwarded on Env for exactly
	// this, rather than hardcoding the github/jira arm: a skewed local or
	// forgejo tracker would otherwise render a self-contradictory prompt (e.g.
	// ISSUE_TRACKER_GITHUB alongside PR_BODY_LOCAL_NOREF).
	itRead, itWrite, itFiler := e.TrackerAxisRead, e.TrackerAxisWrite, e.TrackerAxisFiler
	if itRead == "" {
		itRead, itWrite, itFiler = issueTrackerAxisFallback(e.IssueTracker)
	}

	// researchForceRelay is the ADR 0041 research special-case: a research
	// dispatch with the Filer provisioned always uses the relay form for both
	// the verdict-comment write step and the filer's own write mechanism --
	// unconditionally, with no ORCHESTRATOR_ENABLED condition and regardless of
	// BOX_WRITE_ENABLED. DispatchKind is fixed for a whole Assemble() call, so
	// this can only be true while rendering research-path fragments. kind
	// defaults the same way assemble.go/validate.go do, since DispatchKind is
	// forwarded as the empty string for the default case.
	kind := e.DispatchKind
	if kind == "" {
		kind = defaultDispatchKind
	}
	researchForceRelay := kind == "research" && e.FilerEnabled

	// The issue-read step gate: exactly one of
	// ISSUE_TRACKER_GITHUB/_LOCAL/_FORGEJO fires, selected by itRead above.
	g["ISSUE_TRACKER_GITHUB"] = itRead == "GITHUB"
	g["ISSUE_TRACKER_LOCAL"] = itRead == "LOCAL"
	g["ISSUE_TRACKER_FORGEJO"] = itRead == "FORGEJO"

	// The issue-blocked-comment/research-verdict write-step gates: a tracker
	// with a direct write-step path (itWrite non-empty) forks on
	// BOX_WRITE_ENABLED between the _READWRITE and _READONLY arm; local
	// (itWrite empty) renders neither pair, since its write step always goes
	// through the relay form. These gates also drive the research-path
	// research-verdict fragments, so researchForceRelay (ADR 0041) flips a
	// would-be _READWRITE case to its _READONLY sibling.
	g["ISSUE_TRACKER_GITHUB_READWRITE"] = itWrite == "GITHUB" && e.BoxWriteEnabled && !researchForceRelay
	g["ISSUE_TRACKER_GITHUB_READONLY"] = itWrite == "GITHUB" && (!e.BoxWriteEnabled || researchForceRelay)
	g["ISSUE_TRACKER_FORGEJO_READWRITE"] = itWrite == "FORGEJO" && e.BoxWriteEnabled && !researchForceRelay
	g["ISSUE_TRACKER_FORGEJO_READONLY"] = itWrite == "FORGEJO" && (!e.BoxWriteEnabled || researchForceRelay)

	// The filer's write-mechanism gates: on a work dispatch, relay activates
	// only on read-only (BOX_WRITE_ENABLED absent) plus the orchestrator gate;
	// every other combination keeps the direct gh/fj path, forked further on
	// itFiler. On a research dispatch (ADR 0041) relay activates
	// unconditionally instead -- no orchestrator condition, and in read-write
	// mode too -- so that case is checked first. Both stay off when the filer
	// isn't configured at all.
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
	// The write mechanism itself (host-mediated SPINDRIFT_ISSUE_INTENT relay
	// vs. direct gh/fj) is identical between work and research relay, which is
	// why FILER_FILE_RELAY above stays kind-agnostic and keeps driving every
	// other relay-gated row. But the label the launcher applies host-side once
	// it files each relayed issue differs by kind -- agent-review-finding for
	// work, agent-research-finding for research -- and filer-label-relay.md's
	// prose names it explicitly. These two gates are the kind-split view of
	// that same filerFileRelay boolean: mutually exclusive and together exactly
	// equal to it, since researchForceRelay implies filerFileRelay.
	g["FILER_FILE_RELAY_RESEARCH"] = researchForceRelay
	g["FILER_FILE_RELAY_WORK"] = filerFileRelay && !researchForceRelay
	g["FILER_FILE_DIRECT_GH"] = filerFileDirectGH
	g["FILER_FILE_DIRECT_FORGEJO"] = filerFileDirectForgejo

	// file-issues-direct.md renders whenever either direct fork above is on.
	g["FILER_FILE_DIRECT_ANY"] = filerFileDirectGH || filerFileDirectForgejo

	// The PR-body ticket-reference gates: exactly one is ever on, picked from
	// ISSUE_TRACKER x LOCAL_ISSUE_REFERENCE. jira falls into github's branch.
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

// issueTrackerAxisFallback maps a raw IssueTracker value onto its three
// gate-family suffixes -- the same mapping nix now performs at eval time for
// TrackerAxisRead/Write/Filer. Used only by trackerGates's version-skew
// fallback above; jira shares github's arm since it rides the same in-box
// reachability.
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
