package forge

import "net/http"

var _ CodeForge = (*CodeForgeFake)(nil)

var _ BranchProtectionForge = (*CodeForgeFake)(nil)

// CodeForgeFake is the CodeForge-capability slice of Fake. It embeds the same
// *core instance that Fake and IssueTrackerFake embed, so mu, prStates and
// LandingCallLog are shared state across all three.
type CodeForgeFake struct {
	*core

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
	// DeleteBranchErr, if non-nil, is returned by every DeleteBranch call that
	// passes validation; the branch is then left in place.
	DeleteBranchErr error
	// DeletedBranches records every branch DeleteBranch removed, in order.
	DeletedBranches []string
}

// BranchExists returns BranchExistsErr if set, else the result scripted by
// SetBranchExists, which is false for an unscripted branch.
func (cf *CodeForgeFake) BranchExists(branch string) (bool, error) {
	var out bool
	err := cf.read(pathBranch, &out, func() (any, error) {
		cf.mu.Lock()
		defer cf.mu.Unlock()
		if cf.BranchExistsErr != nil {
			return nil, cf.BranchExistsErr
		}
		return cf.branchExists[branch], nil
	})
	return out, err
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
	var out bool
	err := cf.read(pathBranchProtected, &out, func() (any, error) {
		cf.mu.Lock()
		defer cf.mu.Unlock()
		if err := cf.branchProtectedErr[branch]; err != nil {
			return nil, err
		}
		return cf.branchProtected[branch], nil
	})
	return out, err
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

// Merge runs its whole mutation and recording inside the backend's
// serve closure, so the state a later read observes was written behind the
// Connector.
func (cf *CodeForgeFake) Merge(url string) error {
	cf.Fire("Merge")
	return cf.call(http.MethodPost, pathMerge, nil, func() (any, error) {
		cf.mu.Lock()
		defer cf.mu.Unlock()
		cf.LandingCallLog = append(cf.LandingCallLog, "Merge:"+url)
		if err := popErr(&cf.MergeErrs, cf.MergeErr); err != nil {
			return nil, err
		}
		cf.Merged = url
		cf.prStates[url] = PRMerged
		return nil, nil
	})
}

// Rebase records url and returns the scripted error, crossing the Connector.
func (cf *CodeForgeFake) Rebase(url string) error {
	cf.Fire("Rebase")
	return cf.call(http.MethodPost, pathRebase, nil, func() (any, error) {
		cf.mu.Lock()
		defer cf.mu.Unlock()
		cf.RebasedURLs = append(cf.RebasedURLs, url)
		return nil, popErr(&cf.RebaseErrs, cf.RebaseErr)
	})
}

// Probe returns the scripted repo or error. It duplicates Fake's identical
// method so that CodeForgeFake satisfies CodeForge on its own.
func (cf *CodeForgeFake) Probe() (string, error) { return cf.probe(pathForgeProbe) }

// DeleteBranch refuses an invalid branch (ValidateBranchDelete) before crossing
// the Connector, then marks branch as no longer existing so a later
// BranchExists observes the write. Unlike the BranchDeleter contract, deleting
// a branch the Fake was never told exists succeeds; assert DeletedBranches to
// pin which head a caller deleted.
func (cf *CodeForgeFake) DeleteBranch(branch, base string) error {
	if err := ValidateBranchDelete("fake", branch, base); err != nil {
		return err
	}
	return cf.call(http.MethodDelete, pathDeleteBranch, nil, func() (any, error) {
		cf.mu.Lock()
		defer cf.mu.Unlock()
		if cf.DeleteBranchErr != nil {
			return nil, cf.DeleteBranchErr
		}
		cf.DeletedBranches = append(cf.DeletedBranches, branch)
		cf.branchExists[branch] = false
		return nil, nil
	})
}
