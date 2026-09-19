package forge

var _ CodeForge = (*CodeForgeFake)(nil)

var _ BranchProtectionForge = (*CodeForgeFake)(nil)

// CodeForgeFake is the CodeForge-capability slice of Fake. It embeds the same
// *core instance that Fake and IssueTrackerFake embed, so mu, prStates and
// LandingCallLog are shared state across all three.
type CodeForgeFake struct {
	*core

	// BranchPrefix's zero value "" matches an unconfigured config.branchPrefix;
	// set it to exercise a real prefix such as "agent/issue-".
	BranchPrefix string
	branchExists map[string]bool
	// BranchExistsErr, if non-nil, is returned by every BranchExists call.
	BranchExistsErr error

	branchProtected    map[string]bool
	branchProtectedErr map[string]error

	// MergeErr, if non-nil, is returned by every Merge call once MergeErrs is drained.
	MergeErr error
	// MergeErrs is a per-call queue drained before MergeErr is checked. A nil
	// entry means success.
	MergeErrs []error
	// Merged holds the URL of the last successful Merge call.
	Merged string
	// RebaseErr, if non-nil, is returned by every Rebase call once RebaseErrs
	// is drained.
	RebaseErr error
	// RebaseErrs is a per-call queue drained before RebaseErr is checked. A nil
	// entry means success.
	RebaseErrs []error
	// RebasedURLs records every URL passed to Rebase, in order.
	RebasedURLs []string
}

// AgentBranch returns BranchPrefix + num.
func (cf *CodeForgeFake) AgentBranch(num string) string {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	return cf.BranchPrefix + num
}

// BranchExists returns BranchExistsErr if set, else the result scripted by
// SetBranchExists, which is false for an unscripted branch.
func (cf *CodeForgeFake) BranchExists(branch string) (bool, error) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if cf.BranchExistsErr != nil {
		return false, cf.BranchExistsErr
	}
	return cf.branchExists[branch], nil
}

// SetBranchExists scripts BranchExists's result for branch.
func (cf *CodeForgeFake) SetBranchExists(branch string, exists bool) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	cf.branchExists[branch] = exists
}

// BranchProtected returns the per-branch error set by SetBranchProtectedErr if
// there is one, else the result scripted by SetBranchProtected, which is false
// for an unscripted branch.
func (cf *CodeForgeFake) BranchProtected(branch string) (bool, error) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if err := cf.branchProtectedErr[branch]; err != nil {
		return false, err
	}
	return cf.branchProtected[branch], nil
}

// SetBranchProtected scripts BranchProtected's result for branch.
func (cf *CodeForgeFake) SetBranchProtected(branch string, protected bool) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	cf.branchProtected[branch] = protected
}

// SetBranchProtectedErr scripts BranchProtected to return err for branch. It
// takes precedence over any SetBranchProtected result for that branch, so it
// models a failed probe rather than a definitive "not protected".
func (cf *CodeForgeFake) SetBranchProtectedErr(branch string, err error) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	cf.branchProtectedErr[branch] = err
}

func (cf *CodeForgeFake) Merge(url string) error {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	cf.LandingCallLog = append(cf.LandingCallLog, "Merge:"+url)
	if len(cf.MergeErrs) > 0 {
		err := cf.MergeErrs[0]
		cf.MergeErrs = cf.MergeErrs[1:]
		if err != nil {
			return err
		}
		cf.Merged = url
		cf.prStates[url] = PRMerged
		return nil
	}
	if cf.MergeErr != nil {
		return cf.MergeErr
	}
	cf.Merged = url
	cf.prStates[url] = PRMerged
	return nil
}

func (cf *CodeForgeFake) Rebase(url string) error {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	cf.RebasedURLs = append(cf.RebasedURLs, url)
	if len(cf.RebaseErrs) > 0 {
		err := cf.RebaseErrs[0]
		cf.RebaseErrs = cf.RebaseErrs[1:]
		return err
	}
	return cf.RebaseErr
}

// Probe returns the scripted repo or error. It duplicates Fake's identical
// method so that CodeForgeFake satisfies CodeForge on its own.
func (cf *CodeForgeFake) Probe() (string, error) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if cf.ProbeErr != nil {
		return "", cf.ProbeErr
	}
	return cf.ProbeRepo, nil
}
