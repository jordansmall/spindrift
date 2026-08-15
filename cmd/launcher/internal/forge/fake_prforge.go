package forge

import "fmt"

var _ PRForge = (*PRForgeFake)(nil)

// PRForgeFake is the PR-forge-capability slice of Fake, holding every field
// the PRForge surface reads or writes. It embeds *core — see core's doc
// comment for the admission rule — so its methods can reach mu/prStates/
// LandingCallLog/etc. directly, the same shared core instance Fake,
// IssueTrackerFake, and CodeForgeFake also embed.
type PRForgeFake struct {
	// *core is the shared substrate promoted through to PRForgeFake — see
	// core's doc comment for the admission rule.
	*core

	prs             map[string]PR             // URL → PR
	branchPRs       map[string]string         // branch → PR URL
	mergeableStates map[string]MergeableState // URL → scripted Mergeable result
	needsUpdate     map[string]bool           // URL → scripted NeedsUpdate result
	checkQ          map[string][]RollupState
	checkErrQ       map[string][]error  // per-call error queue; nil entry = consult checkQ
	prFiles         map[string][]string // URL → scripted ListPRFiles result
	headSHAQ        map[string][]string // URL → scripted HeadCommitSHA queue
	headSHACounter  int                 // used to synthesize a fresh SHA once headSHAQ[url] is exhausted

	// HeadCommitSHAErr, if non-nil, is returned by every HeadCommitSHA call.
	HeadCommitSHAErr error

	failureDetail map[string]string // URL → scripted FailureDetail result
	// FailureDetailErr, if non-nil, is returned by every FailureDetail call.
	FailureDetailErr error

	// PRStateErr, if non-nil, is returned by every PRState call (simulating a
	// push-only Code Forge, where PR state has no meaning).
	PRStateErr error

	// PRFilesErr, if non-nil, is returned by every ListPRFiles call.
	PRFilesErr error

	// OpenPRForBranchErr, if non-nil, is returned by every OpenPRForBranch
	// call (simulating a transient forge lookup failure, distinct from "no
	// open PR yet") after OpenPRForBranchErrs is drained.
	OpenPRForBranchErr error

	// OpenPRForBranchErrs is a per-call queue drained before
	// OpenPRForBranchErr is checked. A nil entry means "fall through to the
	// normal branch->PR lookup"; a non-nil entry is returned as the error.
	OpenPRForBranchErrs []error

	// NeedsUpdateErr, if non-nil, is returned by every NeedsUpdate call.
	NeedsUpdateErr error

	// AutoMergeAllowed controls what CanAutoMerge returns (default false).
	AutoMergeAllowed bool
	// AutoMergeErr, if non-nil, is returned by CanAutoMerge.
	AutoMergeErr error
	// EnqueueAutoMergeErr, if non-nil, is returned by EnqueueAutoMerge.
	EnqueueAutoMergeErr error
	// EnqueueAutoMergeCalls records all PR URLs passed to EnqueueAutoMerge.
	EnqueueAutoMergeCalls []string

	// MarkReadyErr, if non-nil, is returned by MarkReady.
	MarkReadyErr error
	// MarkReadyCalls records all PR URLs passed to MarkReady, in order.
	MarkReadyCalls []string

	// MarkDraftErr, if non-nil, is returned by MarkDraft.
	MarkDraftErr error
	// MarkDraftCalls records all PR URLs passed to MarkDraft, in order.
	MarkDraftCalls []string
}

// SetPR registers a PR reachable by the given head branch name.
func (pf *PRForgeFake) SetPR(branch string, pr PR) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.prs[pr.URL] = pr
	pf.branchPRs[branch] = pr.URL
	if _, ok := pf.prStates[pr.URL]; !ok {
		pf.prStates[pr.URL] = PROpen
	}
}

// SetPRState overrides the canonical state of a known PR.
func (pf *PRForgeFake) SetPRState(url string, state PRState) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.prStates[url] = state
}

// SetMergeableState scripts the MergeableState Mergeable returns for url.
func (pf *PRForgeFake) SetMergeableState(url string, state MergeableState) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.mergeableStates[url] = state
}

// Mergeable returns the scripted MergeableState for url, or MergeableUnknown
// when nothing was scripted.
func (pf *PRForgeFake) Mergeable(url string) (MergeableState, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if s, ok := pf.mergeableStates[url]; ok {
		return s, nil
	}
	return MergeableUnknown, nil
}

// SetNeedsUpdate scripts the NeedsUpdate result for url.
func (pf *PRForgeFake) SetNeedsUpdate(url string, stale bool) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.needsUpdate[url] = stale
}

// NeedsUpdate returns the scripted staleness for url (false when nothing was
// scripted), or NeedsUpdateErr if set.
func (pf *PRForgeFake) NeedsUpdate(url string) (bool, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if pf.NeedsUpdateErr != nil {
		return false, pf.NeedsUpdateErr
	}
	return pf.needsUpdate[url], nil
}

// SetCheckStates scripts the sequence of RollupState values returned by
// successive CheckState calls for the given PR URL.
func (pf *PRForgeFake) SetCheckStates(url string, states []RollupState) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.checkQ[url] = append([]RollupState(nil), states...)
}

// SetCheckStateErrors scripts a per-call error queue for CheckState. Each
// entry is consumed in order before the state queue is consulted. A nil entry
// means "no error for this call — fall through to the state queue."
func (pf *PRForgeFake) SetCheckStateErrors(url string, errs []error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.checkErrQ[url] = append([]error(nil), errs...)
}

// SetPRFiles scripts the ListPRFiles result for the given PR URL.
func (pf *PRForgeFake) SetPRFiles(url string, files []string) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.prFiles[url] = append([]string(nil), files...)
}

func (pf *PRForgeFake) ListPRFiles(url string) ([]string, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if pf.PRFilesErr != nil {
		return nil, pf.PRFilesErr
	}
	out := make([]string, len(pf.prFiles[url]))
	copy(out, pf.prFiles[url])
	return out, nil
}

func (pf *PRForgeFake) OpenPRForBranch(branch string) (PR, bool, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if len(pf.OpenPRForBranchErrs) > 0 {
		err := pf.OpenPRForBranchErrs[0]
		pf.OpenPRForBranchErrs = pf.OpenPRForBranchErrs[1:]
		if err != nil {
			return PR{}, false, err
		}
	} else if pf.OpenPRForBranchErr != nil {
		return PR{}, false, pf.OpenPRForBranchErr
	}
	url, ok := pf.branchPRs[branch]
	if !ok {
		return PR{}, false, nil
	}
	if pf.prStates[url] != PROpen {
		return PR{}, false, nil
	}
	pr, ok := pf.prs[url]
	if !ok {
		return PR{}, false, nil
	}
	return pr, true, nil
}

func (pf *PRForgeFake) PRForBranch(branch string) (string, bool, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	url, ok := pf.branchPRs[branch]
	if !ok {
		return "", false, nil
	}
	return url, true, nil
}

func (pf *PRForgeFake) PRState(url string) (PRState, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if pf.PRStateErr != nil {
		return "", pf.PRStateErr
	}
	s, ok := pf.prStates[url]
	if !ok {
		return "", fmt.Errorf("PR %s not found", url)
	}
	return s, nil
}

// CheckState pops the next scripted entry for url. The error queue is
// consulted first: a non-nil entry returns StateNone plus that error; a nil
// entry falls through to the state queue. When both queues are exhausted it
// returns StateNone (simulating a PR with no checks registered).
func (pf *PRForgeFake) CheckState(url string) (RollupState, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if eq := pf.checkErrQ[url]; len(eq) > 0 {
		entry := eq[0]
		pf.checkErrQ[url] = eq[1:]
		if entry != nil {
			return StateNone, entry
		}
		// nil entry: fall through to state queue
	}
	q := pf.checkQ[url]
	if len(q) == 0 {
		return StateNone, nil
	}
	s := q[0]
	pf.checkQ[url] = q[1:]
	return s, nil
}

// SetHeadCommitSHAs scripts the sequence of head-commit SHAs returned by
// successive HeadCommitSHA calls for the given PR URL. Once the queue is
// exhausted (including when nothing was ever scripted), each call returns a
// fresh, always-distinct value — modeling the common case where a push
// genuinely advanced the head — so only a test that explicitly repeats a SHA
// models a no-op fix pass that left the head unchanged.
func (pf *PRForgeFake) SetHeadCommitSHAs(url string, shas []string) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.headSHAQ[url] = append([]string(nil), shas...)
}

// HeadCommitSHA pops the next scripted entry for url, or synthesizes a fresh,
// always-distinct value once the queue is exhausted.
func (pf *PRForgeFake) HeadCommitSHA(url string) (string, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if pf.HeadCommitSHAErr != nil {
		return "", pf.HeadCommitSHAErr
	}
	if q := pf.headSHAQ[url]; len(q) > 0 {
		s := q[0]
		pf.headSHAQ[url] = q[1:]
		return s, nil
	}
	pf.headSHACounter++
	return fmt.Sprintf("fake-sha-%d", pf.headSHACounter), nil
}

// SetFailureDetail scripts the FailureDetail result for the given PR URL.
func (pf *PRForgeFake) SetFailureDetail(url, detail string) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.failureDetail[url] = detail
}

// FailureDetail returns the scripted detail for url, or "" when nothing was
// scripted — mirroring the best-effort contract of the real adapter, where a
// PR with no failing checks yields no detail rather than an error.
func (pf *PRForgeFake) FailureDetail(url string) (string, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if pf.FailureDetailErr != nil {
		return "", pf.FailureDetailErr
	}
	return pf.failureDetail[url], nil
}

func (pf *PRForgeFake) CanAutoMerge() (bool, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if pf.AutoMergeErr != nil {
		return false, pf.AutoMergeErr
	}
	return pf.AutoMergeAllowed, nil
}

func (pf *PRForgeFake) EnqueueAutoMerge(prURL string) error {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.LandingCallLog = append(pf.LandingCallLog, "EnqueueAutoMerge:"+prURL)
	pf.EnqueueAutoMergeCalls = append(pf.EnqueueAutoMergeCalls, prURL)
	return pf.EnqueueAutoMergeErr
}

// MarkReady records the call to MarkReadyCalls, observable in tests via
// that log rather than a stored draft-flag flip (the Fake, like the real
// adapters, no longer tracks draft state on the stored PR).
func (pf *PRForgeFake) MarkReady(prURL string) error {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.LandingCallLog = append(pf.LandingCallLog, "MarkReady:"+prURL)
	pf.MarkReadyCalls = append(pf.MarkReadyCalls, prURL)
	return pf.MarkReadyErr
}

// MarkDraft records the call to MarkDraftCalls — the inverse of MarkReady.
func (pf *PRForgeFake) MarkDraft(prURL string) error {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.LandingCallLog = append(pf.LandingCallLog, "MarkDraft:"+prURL)
	pf.MarkDraftCalls = append(pf.MarkDraftCalls, prURL)
	return pf.MarkDraftErr
}
