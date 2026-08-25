package forge

// Fake is an in-memory Client for unit tests. All methods are safe for
// concurrent use. CheckState pops from a scripted RollupState queue so polling
// tests need no real sleeps.
//
// Fake is a composite of five structs: *core plus the four
// capability slices embedded below (*IssueTrackerFake, *CodeForgeFake,
// *PRForgeFake, *HostMediationFake). No two of those five may declare a
// field or method with the same name, except where Probe's hand-written
// override below resolves the one existing collision (IssueTrackerFake and
// CodeForgeFake both define Probe at equal depth). Any other collision
// either fails to compile as an ambiguous selector, or — if the names happen
// to sit at different embedding depths — silently shadows the promoted
// field instead, the way a prior slice's Mark/EnqueueAutoMerge fields
// briefly did before being hoisted into core.
type Fake struct {
	// *core is the shared substrate promoted through to Fake — see core's
	// doc comment for the admission rule.
	*core

	// *IssueTrackerFake is the tracker-capability slice, embedded (in
	// addition to *core above) so Fake's own direct *core embed stays the
	// unambiguous shallowest path to core's fields — see the fake.go package
	// doc comment on Fake for why both embeds are required.
	*IssueTrackerFake

	// *CodeForgeFake is the code-forge-capability slice, embedded alongside
	// *core and *IssueTrackerFake above for the same reason — see
	// IssueTrackerFake's comment.
	*CodeForgeFake

	// *PRForgeFake is the PR-forge-capability slice, embedded alongside
	// *core, *IssueTrackerFake, and *CodeForgeFake above for the same reason
	// — see IssueTrackerFake's comment.
	*PRForgeFake

	// *HostMediationFake is the host-mediation-capability slice — the
	// relay/draft-PR/post-issue/landing-ref/landing-containment/
	// integration-tip surfaces only reachable through the AsLocal(),
	// AsGithubReadOnly(), and AsIssueFiler() wrappers below — embedded
	// alongside *core, *IssueTrackerFake, *CodeForgeFake, and *PRForgeFake
	// above for the same reason — see IssueTrackerFake's comment.
	*HostMediationFake
}

// NewFake returns an empty Fake client. labels configures the
// DispatchState-to-label mapping the same way production adapters (Exec,
// Local, Jira) take it as a constructor argument; omit it for tests that
// never exercise ListIssues(state) or TransitionState.
func NewFake(labels ...DispatchLabels) *Fake {
	var l DispatchLabels
	if len(labels) > 0 {
		l = labels[0]
	}
	c := &core{prStates: map[string]PRState{}}
	return &Fake{
		core: c,
		IssueTrackerFake: &IssueTrackerFake{
			core:   c,
			labels: l,
			issues: map[string]Issue{},
		},
		CodeForgeFake: &CodeForgeFake{
			core:               c,
			branchExists:       map[string]bool{},
			branchProtected:    map[string]bool{},
			branchProtectedErr: map[string]error{},
		},
		PRForgeFake: &PRForgeFake{
			core:            c,
			prs:             map[string]PR{},
			branchPRs:       map[string]string{},
			mergeableStates: map[string]MergeableState{},
			needsUpdate:     map[string]bool{},
			checkQ:          map[string][]RollupState{},
			checkErrQ:       map[string][]error{},
			prFiles:         map[string][]string{},
			headSHAQ:        map[string][]string{},
			failureDetail:   map[string]string{},
		},
		HostMediationFake: &HostMediationFake{core: c},
	}
}

// Probe is the composite's sole hand-written method: it disambiguates the
// Probe collision between the embedded *IssueTrackerFake and *CodeForgeFake
// (both define Probe at equal depth, an ambiguous selector without this
// override) by resolving through Fake's own direct *core embed, the
// shallowest path both capability slices' Probe implementations also read.
func (f *Fake) Probe() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ProbeErr != nil {
		return "", f.ProbeErr
	}
	return f.ProbeRepo, nil
}

var _ LandingRecorder = (*Fake)(nil)

var _ IssueCloser = (*Fake)(nil)

var _ MergeCloser = (*Fake)(nil)

var _ AbandonedFlagger = (*Fake)(nil)
