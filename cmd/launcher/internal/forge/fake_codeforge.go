package forge

// AgentBranch returns BranchPrefix + num.
func (f *Fake) AgentBranch(num string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.BranchPrefix + num
}

// BranchExists returns the scripted result set by SetBranchExists (false for
// an unscripted branch), or BranchExistsErr if set.
func (f *Fake) BranchExists(branch string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BranchExistsErr != nil {
		return false, f.BranchExistsErr
	}
	return f.branchExists[branch], nil
}

// SetBranchExists scripts BranchExists's result for branch. Unset branches
// default to false (not found).
func (f *Fake) SetBranchExists(branch string, exists bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branchExists[branch] = exists
}

func (f *Fake) Merge(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LandingCallLog = append(f.LandingCallLog, "Merge:"+url)
	if len(f.MergeErrs) > 0 {
		err := f.MergeErrs[0]
		f.MergeErrs = f.MergeErrs[1:]
		if err != nil {
			return err
		}
		f.Merged = url
		f.prStates[url] = PRMerged
		return nil
	}
	if f.MergeErr != nil {
		return f.MergeErr
	}
	f.Merged = url
	f.prStates[url] = PRMerged
	return nil
}

func (f *Fake) Rebase(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RebasedURLs = append(f.RebasedURLs, url)
	if len(f.RebaseErrs) > 0 {
		err := f.RebaseErrs[0]
		f.RebaseErrs = f.RebaseErrs[1:]
		return err
	}
	return f.RebaseErr
}
