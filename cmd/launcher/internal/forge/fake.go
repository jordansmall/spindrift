package forge

// Fake is an in-memory Client for unit tests, safe for concurrent use.
// CheckState pops from a scripted RollupState queue, so polling tests need no
// real sleeps. Fake embeds *core plus the four capability slices below, and no
// two of those five may declare a name in common. Probe overrides the one
// collision; a new one breaks the build or silently shadows the promoted field.
type Fake struct {
	// Every capability slice below embeds core too, so Fake embeds it directly
	// to keep the shallowest unambiguous path to core's fields.
	*core

	*IssueTrackerFake

	*CodeForgeFake

	*PRForgeFake

	// HostMediationFake's methods are reachable only through the AsLocal(),
	// AsGithubReadOnly(), and AsIssueFiler() wrappers.
	*HostMediationFake
}

// NewFake returns an empty Fake client. labels configures the
// DispatchState-to-label mapping; omit it for tests that never exercise
// ListIssues(state) or TransitionState.
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

// Probe resolves through Fake's own direct *core embed to break the ambiguous
// selector between the embedded *IssueTrackerFake and *CodeForgeFake, which
// both define Probe at equal depth.
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
