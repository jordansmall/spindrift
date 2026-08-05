package forge

var _ CodeForge = (*CodeForgeFake)(nil)

// CodeForgeFake is the code-forge-capability slice of Fake, holding every
// field the CodeForge surface reads or writes. It embeds *core — see core's
// doc comment for the admission rule — so its methods can reach mu/prStates/
// LandingCallLog/etc. directly, the same shared core instance Fake and
// IssueTrackerFake also embed.
type CodeForgeFake struct {
	// *core is the shared substrate promoted through to CodeForgeFake — see
	// core's doc comment for the admission rule.
	*core

	// BranchPrefix is baked into AgentBranch's output. Zero value "" matches
	// an unconfigured config.branchPrefix; set explicitly to exercise a real
	// prefix (e.g. "agent/issue-").
	BranchPrefix string
	branchExists map[string]bool // branch → scripted BranchExists result
	// BranchExistsErr, if non-nil, is returned by every BranchExists call.
	BranchExistsErr error

	// MergeErr, if non-nil, is returned by every Merge call (after MergeErrs is drained).
	MergeErr error
	// MergeErrs is a per-call queue drained before MergeErr is checked.
	// A nil entry means success; a non-nil entry is returned as the error.
	MergeErrs []error
	// Merged is set to the URL of the last successful Merge call.
	Merged string
	// RebaseErr, if non-nil, is returned by every Rebase call (after
	// RebaseErrs is drained).
	RebaseErr error
	// RebaseErrs is a per-call queue drained before RebaseErr is checked.
	// A nil entry means success; a non-nil entry is returned as the error.
	RebaseErrs []error
	// RebasedURLs records all URLs passed to Rebase in order.
	RebasedURLs []string
}

// AgentBranch returns BranchPrefix + num.
func (cf *CodeForgeFake) AgentBranch(num string) string {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	return cf.BranchPrefix + num
}

// BranchExists returns the scripted result set by SetBranchExists (false for
// an unscripted branch), or BranchExistsErr if set.
func (cf *CodeForgeFake) BranchExists(branch string) (bool, error) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if cf.BranchExistsErr != nil {
		return false, cf.BranchExistsErr
	}
	return cf.branchExists[branch], nil
}

// SetBranchExists scripts BranchExists's result for branch. Unset branches
// default to false (not found).
func (cf *CodeForgeFake) SetBranchExists(branch string, exists bool) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	cf.branchExists[branch] = exists
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

// Probe locks cf's own embedded *core and returns the scripted repo/error —
// CodeForgeFake's own copy of Fake's identically-bodied Probe, needed so
// CodeForgeFake independently satisfies CodeForge (var _ CodeForge above),
// matching IssueTrackerFake's own Probe from the tracker-capability slice.
func (cf *CodeForgeFake) Probe() (string, error) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if cf.ProbeErr != nil {
		return "", cf.ProbeErr
	}
	return cf.ProbeRepo, nil
}
