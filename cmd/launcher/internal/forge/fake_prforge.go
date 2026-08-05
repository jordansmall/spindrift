package forge

import "fmt"

// SetMergeableState scripts the MergeableState Mergeable returns for url.
func (f *Fake) SetMergeableState(url string, state MergeableState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mergeableStates[url] = state
}

// Mergeable returns the scripted MergeableState for url, or MergeableUnknown
// when nothing was scripted.
func (f *Fake) Mergeable(url string) (MergeableState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.mergeableStates[url]; ok {
		return s, nil
	}
	return MergeableUnknown, nil
}

// SetNeedsUpdate scripts the NeedsUpdate result for url.
func (f *Fake) SetNeedsUpdate(url string, stale bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.needsUpdate[url] = stale
}

// NeedsUpdate returns the scripted staleness for url (false when nothing was
// scripted), or NeedsUpdateErr if set.
func (f *Fake) NeedsUpdate(url string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.NeedsUpdateErr != nil {
		return false, f.NeedsUpdateErr
	}
	return f.needsUpdate[url], nil
}

// SetCheckStates scripts the sequence of RollupState values returned by
// successive CheckState calls for the given PR URL.
func (f *Fake) SetCheckStates(url string, states []RollupState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkQ[url] = append([]RollupState(nil), states...)
}

// SetCheckStateErrors scripts a per-call error queue for CheckState. Each
// entry is consumed in order before the state queue is consulted. A nil entry
// means "no error for this call — fall through to the state queue."
func (f *Fake) SetCheckStateErrors(url string, errs []error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkErrQ[url] = append([]error(nil), errs...)
}

// SetPRFiles scripts the ListPRFiles result for the given PR URL.
func (f *Fake) SetPRFiles(url string, files []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prFiles[url] = append([]string(nil), files...)
}

func (f *Fake) ListPRFiles(url string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PRFilesErr != nil {
		return nil, f.PRFilesErr
	}
	out := make([]string, len(f.prFiles[url]))
	copy(out, f.prFiles[url])
	return out, nil
}

func (f *Fake) OpenPRForBranch(branch string) (PR, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.OpenPRForBranchErrs) > 0 {
		err := f.OpenPRForBranchErrs[0]
		f.OpenPRForBranchErrs = f.OpenPRForBranchErrs[1:]
		if err != nil {
			return PR{}, false, err
		}
	} else if f.OpenPRForBranchErr != nil {
		return PR{}, false, f.OpenPRForBranchErr
	}
	url, ok := f.branchPRs[branch]
	if !ok {
		return PR{}, false, nil
	}
	if f.prStates[url] != PROpen {
		return PR{}, false, nil
	}
	pr, ok := f.prs[url]
	if !ok {
		return PR{}, false, nil
	}
	return pr, true, nil
}

func (f *Fake) PRForBranch(branch string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	url, ok := f.branchPRs[branch]
	if !ok {
		return "", false, nil
	}
	return url, true, nil
}

func (f *Fake) PRState(url string) (PRState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PRStateErr != nil {
		return "", f.PRStateErr
	}
	s, ok := f.prStates[url]
	if !ok {
		return "", fmt.Errorf("PR %s not found", url)
	}
	return s, nil
}

// CheckState pops the next scripted entry for url. The error queue is
// consulted first: a non-nil entry returns StateNone plus that error; a nil
// entry falls through to the state queue. When both queues are exhausted it
// returns StateNone (simulating a PR with no checks registered).
func (f *Fake) CheckState(url string) (RollupState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if eq := f.checkErrQ[url]; len(eq) > 0 {
		entry := eq[0]
		f.checkErrQ[url] = eq[1:]
		if entry != nil {
			return StateNone, entry
		}
		// nil entry: fall through to state queue
	}
	q := f.checkQ[url]
	if len(q) == 0 {
		return StateNone, nil
	}
	s := q[0]
	f.checkQ[url] = q[1:]
	return s, nil
}

// SetHeadCommitSHAs scripts the sequence of head-commit SHAs returned by
// successive HeadCommitSHA calls for the given PR URL. Once the queue is
// exhausted (including when nothing was ever scripted), each call returns a
// fresh, always-distinct value — modeling the common case where a push
// genuinely advanced the head — so only a test that explicitly repeats a SHA
// models a no-op fix pass that left the head unchanged.
func (f *Fake) SetHeadCommitSHAs(url string, shas []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headSHAQ[url] = append([]string(nil), shas...)
}

// HeadCommitSHA pops the next scripted entry for url, or synthesizes a fresh,
// always-distinct value once the queue is exhausted.
func (f *Fake) HeadCommitSHA(url string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.HeadCommitSHAErr != nil {
		return "", f.HeadCommitSHAErr
	}
	if q := f.headSHAQ[url]; len(q) > 0 {
		s := q[0]
		f.headSHAQ[url] = q[1:]
		return s, nil
	}
	f.headSHACounter++
	return fmt.Sprintf("fake-sha-%d", f.headSHACounter), nil
}

// SetFailureDetail scripts the FailureDetail result for the given PR URL.
func (f *Fake) SetFailureDetail(url, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failureDetail[url] = detail
}

// FailureDetail returns the scripted detail for url, or "" when nothing was
// scripted — mirroring the best-effort contract of the real adapter, where a
// PR with no failing checks yields no detail rather than an error.
func (f *Fake) FailureDetail(url string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailureDetailErr != nil {
		return "", f.FailureDetailErr
	}
	return f.failureDetail[url], nil
}

func (f *Fake) CanAutoMerge() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.AutoMergeErr != nil {
		return false, f.AutoMergeErr
	}
	return f.AutoMergeAllowed, nil
}

func (f *Fake) EnqueueAutoMerge(prURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LandingCallLog = append(f.LandingCallLog, "EnqueueAutoMerge:"+prURL)
	f.EnqueueAutoMergeCalls = append(f.EnqueueAutoMergeCalls, prURL)
	return f.EnqueueAutoMergeErr
}

func (f *Fake) MarkReady(prURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LandingCallLog = append(f.LandingCallLog, "MarkReady:"+prURL)
	f.MarkReadyCalls = append(f.MarkReadyCalls, prURL)
	return f.MarkReadyErr
}

func (f *Fake) MarkDraft(prURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LandingCallLog = append(f.LandingCallLog, "MarkDraft:"+prURL)
	f.MarkDraftCalls = append(f.MarkDraftCalls, prURL)
	return f.MarkDraftErr
}
