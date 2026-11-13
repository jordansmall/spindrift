package forge

import (
	"fmt"
	"net/http"
)

var _ PRForge = (*PRForgeFake)(nil)

// PRForgeFake is the PR-forge slice of Fake. Its embedded *core is the same
// instance Fake, IssueTrackerFake, and CodeForgeFake embed, so state written
// through one capability is visible from the others.
type PRForgeFake struct {
	*core

	prs             map[string]PR
	branchPRs       map[string]string // keyed by branch name, valued by PR URL
	mergeableStates map[string]MergeableState
	mergeableQ      map[string][]MergeableState
	needsUpdate     map[string]bool
	drafts          map[string]bool // keyed by PR URL; internal-only, forge.PR carries no draft field (#2503)
	checkQ          map[string][]RollupState
	checkErrQ       map[string][]error // a nil entry falls through to checkQ
	prFiles         map[string][]string
	headSHAQ        map[string][]string
	mergeCommits    map[string]string
	headSHACounter  int // HeadCommitSHA counts it up to synthesize a SHA once headSHAQ[url] is exhausted

	// HeadCommitSHAErr, if non-nil, is returned by every HeadCommitSHA call.
	HeadCommitSHAErr error

	failureDetail map[string]string
	// FailureDetailErr, if non-nil, is returned by every FailureDetail call.
	FailureDetailErr error

	// PRStateErr, if non-nil, is returned by every PRState call once PRStateErrs
	// is drained. It models a push-only Code Forge, where PR state has no
	// meaning, or a persistent read failure.
	PRStateErr error

	// PRStateErrs is a per-call queue drained before PRStateErr is checked. A
	// nil entry falls through to the normal state lookup.
	PRStateErrs []error

	// MergeCommitErr, if non-nil, is returned by every MergeCommit call.
	MergeCommitErr error

	// PRFilesErr, if non-nil, is returned by every ListPRFiles call.
	PRFilesErr error

	// PRForBranchErr, if non-nil, is returned by every PRForBranch call once
	// PRForBranchErrs is drained: a lookup failure, distinct from "no PR on the
	// branch".
	PRForBranchErr error

	// PRForBranchErrs is a per-call queue drained before PRForBranchErr is
	// checked. A nil entry falls through to the normal branch lookup.
	PRForBranchErrs []error

	// OpenPRForBranchErr, if non-nil, is returned by every OpenPRForBranch call
	// once OpenPRForBranchErrs is drained. It models a transient lookup
	// failure, which is distinct from "no open PR yet".
	OpenPRForBranchErr error

	// OpenPRForBranchErrs is a per-call queue drained before OpenPRForBranchErr
	// is checked. A nil entry falls through to the normal branch lookup.
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

	// CommentPRErr, if non-nil, is returned by CommentPR.
	CommentPRErr error
	// CommentPRCalls records every CommentPR call, in order.
	CommentPRCalls []CommentPRCall

	// MergeableErr, if non-nil, is returned by every Mergeable call once
	// MergeableCalls exceeds MergeableErrAfter.
	MergeableErr error
	// MergeableErrAfter is how many Mergeable calls succeed before
	// MergeableErr applies.
	MergeableErrAfter int
	// MergeableCalls counts Mergeable calls.
	MergeableCalls int
	// DraftUnmergeable models Forgejo, whose boolean mergeable is false for a
	// draft pull: Mergeable then reports MergeableConflicting while the PR is
	// draft (see SetDraft), whatever state was scripted.
	DraftUnmergeable bool

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

// SetMergeCommit scripts the commit MergeCommit reports for url; unset, it
// reports "" like a forge that names none.
func (pf *PRForgeFake) SetMergeCommit(url, sha string) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.mergeCommits[url] = sha
}

// MergeCommit returns the scripted merge commit for url, or MergeCommitErr.
func (pf *PRForgeFake) MergeCommit(url string) (string, error) {
	var sha string
	err := pf.read(pathMergeCommit, &sha, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		if pf.MergeCommitErr != nil {
			return nil, pf.MergeCommitErr
		}
		return pf.mergeCommits[url], nil
	})
	return sha, err
}

// SetMergeableState scripts the MergeableState Mergeable returns for url.
func (pf *PRForgeFake) SetMergeableState(url string, state MergeableState) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.mergeableStates[url] = state
}

// SetMergeableStates scripts successive Mergeable results for url, consumed
// before the state SetMergeableState scripts.
func (pf *PRForgeFake) SetMergeableStates(url string, states []MergeableState) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.mergeableQ[url] = append([]MergeableState(nil), states...)
}

// Mergeable returns the next queued state for url, else the scripted state, or
// MergeableUnknown when nothing was scripted.
func (pf *PRForgeFake) Mergeable(url string) (MergeableState, error) {
	var s MergeableState
	err := pf.read(pathMergeable, &s, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		pf.MergeableCalls++
		if pf.MergeableErr != nil && pf.MergeableCalls > pf.MergeableErrAfter {
			return nil, pf.MergeableErr
		}
		if pf.DraftUnmergeable && pf.drafts[url] {
			return MergeableConflicting, nil
		}
		if q := pf.mergeableQ[url]; len(q) > 0 {
			pf.mergeableQ[url] = q[1:]
			return q[0], nil
		}
		if s, ok := pf.mergeableStates[url]; ok {
			return s, nil
		}
		return MergeableUnknown, nil
	})
	if err != nil {
		return MergeableUnknown, err
	}
	return s, nil
}

// SetNeedsUpdate scripts the NeedsUpdate result for url.
func (pf *PRForgeFake) SetNeedsUpdate(url string, stale bool) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.needsUpdate[url] = stale
}

// NeedsUpdate returns the scripted staleness for url, false when nothing was
// scripted.
func (pf *PRForgeFake) NeedsUpdate(url string) (bool, error) {
	var stale bool
	err := pf.read(pathNeedsUpdate, &stale, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		if pf.NeedsUpdateErr != nil {
			return nil, pf.NeedsUpdateErr
		}
		return pf.needsUpdate[url], nil
	})
	return stale, err
}

// SetDraft seeds url's draft state, which MarkReady and MarkDraft then flip.
func (pf *PRForgeFake) SetDraft(url string, draft bool) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.drafts[url] = draft
}

// IsDraft reports url's tracked draft state, false when none was seeded or set.
// It has no error return, so a Connector failure panics rather than answering
// "not a draft" for a read that never happened.
func (pf *PRForgeFake) IsDraft(url string) bool {
	var draft bool
	err := pf.read(pathDraft, &draft, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		return pf.drafts[url], nil
	})
	if err != nil {
		panic(fmt.Sprintf("forge: Fake.IsDraft(%s): %v", url, err))
	}
	return draft
}

// SetCheckStates scripts the states successive CheckState calls return for url.
func (pf *PRForgeFake) SetCheckStates(url string, states []RollupState) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.checkQ[url] = append([]RollupState(nil), states...)
}

// SetCheckStateErrors scripts a per-call error queue for CheckState, consumed
// in order before the state queue. A nil entry falls through to that queue.
func (pf *PRForgeFake) SetCheckStateErrors(url string, errs []error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.checkErrQ[url] = append([]error(nil), errs...)
}

// SetPRFiles scripts the ListPRFiles result for url.
func (pf *PRForgeFake) SetPRFiles(url string, files []string) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.prFiles[url] = append([]string(nil), files...)
}

func (pf *PRForgeFake) ListPRFiles(url string) ([]string, error) {
	pf.Fire("ListPRFiles")
	var files []string
	err := pf.read(pathFiles, &files, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		if pf.PRFilesErr != nil {
			return nil, pf.PRFilesErr
		}
		return pf.prFiles[url], nil
	})
	if err != nil {
		return nil, err
	}
	// A scripted empty list decodes as nil through JSON null; keep it non-nil.
	return append([]string{}, files...), nil
}

// openPRAnswer is OpenPRForBranch's (PR, bool) as one JSON answer.
type openPRAnswer struct {
	PR PR
	OK bool
}

func (pf *PRForgeFake) OpenPRForBranch(branch string) (PR, bool, error) {
	pf.Fire("OpenPRForBranch")
	var out openPRAnswer
	err := pf.read(pathPRsForBranch, &out, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		if err := popErr(&pf.OpenPRForBranchErrs, pf.OpenPRForBranchErr); err != nil {
			return nil, err
		}
		url, ok := pf.branchPRs[branch]
		if !ok || pf.prStates[url] != PROpen {
			return openPRAnswer{}, nil
		}
		pr, ok := pf.prs[url]
		return openPRAnswer{pr, ok}, nil
	})
	if err != nil || !out.OK {
		return PR{}, false, err
	}
	return out.PR, true, nil
}

// branchPRAnswer is PRForBranch's (URL, bool) as one JSON answer.
type branchPRAnswer struct {
	URL string
	OK  bool
}

func (pf *PRForgeFake) PRForBranch(branch string) (string, bool, error) {
	var out branchPRAnswer
	err := pf.read(pathPRsForBranch, &out, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		if err := popErr(&pf.PRForBranchErrs, pf.PRForBranchErr); err != nil {
			return nil, err
		}
		url, ok := pf.branchPRs[branch]
		return branchPRAnswer{url, ok}, nil
	})
	if err != nil || !out.OK {
		return "", false, err
	}
	return out.URL, true, nil
}

func (pf *PRForgeFake) PRState(url string) (PRState, error) {
	var s PRState
	err := pf.read(pathPR, &s, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		if err := popErr(&pf.PRStateErrs, pf.PRStateErr); err != nil {
			return nil, err
		}
		s, ok := pf.prStates[url]
		if !ok {
			return nil, fmt.Errorf("PR %s not found", url)
		}
		return s, nil
	})
	if err != nil {
		return "", err
	}
	return s, nil
}

// CheckState pops the next scripted entry for url, consulting the error queue
// first. With both queues exhausted it returns StateNone, the same answer a PR
// with no checks registered gives.
func (pf *PRForgeFake) CheckState(url string) (RollupState, error) {
	pf.Fire("CheckState")
	var s RollupState
	err := pf.read(pathChecks, &s, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		if eq := pf.checkErrQ[url]; len(eq) > 0 {
			entry := eq[0]
			pf.checkErrQ[url] = eq[1:]
			if entry != nil {
				return nil, entry
			}
		}
		q := pf.checkQ[url]
		if len(q) == 0 {
			return StateNone, nil
		}
		s := q[0]
		pf.checkQ[url] = q[1:]
		return s, nil
	})
	if err != nil {
		return StateNone, err
	}
	return s, nil
}

// SetHeadCommitSHAs scripts the SHAs successive HeadCommitSHA calls return for
// url. Once the queue is exhausted, including when nothing was ever scripted,
// each call returns a distinct value, so only a test that repeats a SHA models
// a fix pass that left the head unchanged.
func (pf *PRForgeFake) SetHeadCommitSHAs(url string, shas []string) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.headSHAQ[url] = append([]string(nil), shas...)
}

// HeadCommitSHA pops the next scripted entry for url, or synthesizes a distinct
// value once the queue is exhausted.
func (pf *PRForgeFake) HeadCommitSHA(url string) (string, error) {
	var sha string
	err := pf.read(pathHeadSHA, &sha, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		if pf.HeadCommitSHAErr != nil {
			return nil, pf.HeadCommitSHAErr
		}
		if q := pf.headSHAQ[url]; len(q) > 0 {
			s := q[0]
			pf.headSHAQ[url] = q[1:]
			return s, nil
		}
		pf.headSHACounter++
		return fmt.Sprintf("fake-sha-%d", pf.headSHACounter), nil
	})
	return sha, err
}

// SetFailureDetail scripts the FailureDetail result for url.
func (pf *PRForgeFake) SetFailureDetail(url, detail string) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.failureDetail[url] = detail
}

// FailureDetail returns the scripted detail for url, or "" when nothing was
// scripted. The real adapter is best-effort the same way: a PR with no failing
// checks yields no detail rather than an error.
func (pf *PRForgeFake) FailureDetail(url string) (string, error) {
	var detail string
	err := pf.read(pathChecks, &detail, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		if pf.FailureDetailErr != nil {
			return nil, pf.FailureDetailErr
		}
		return pf.failureDetail[url], nil
	})
	return detail, err
}

func (pf *PRForgeFake) CanAutoMerge() (bool, error) {
	var ok bool
	err := pf.read(pathAutoMerge, &ok, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		if pf.AutoMergeErr != nil {
			return nil, pf.AutoMergeErr
		}
		return pf.AutoMergeAllowed, nil
	})
	return ok, err
}

func (pf *PRForgeFake) EnqueueAutoMerge(prURL string) error {
	return pf.call(http.MethodPost, pathAutoMerge, nil, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		pf.LandingCallLog = append(pf.LandingCallLog, "EnqueueAutoMerge:"+prURL)
		pf.EnqueueAutoMergeCalls = append(pf.EnqueueAutoMergeCalls, prURL)
		return nil, pf.EnqueueAutoMergeErr
	})
}

// MarkReady records the call to MarkReadyCalls and, unless MarkReadyErr fails
// it, clears the PR's draft state (see IsDraft).
func (pf *PRForgeFake) MarkReady(prURL string) error {
	pf.Fire("MarkReady")
	return pf.call(http.MethodPatch, pathDraft, nil, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		pf.LandingCallLog = append(pf.LandingCallLog, "MarkReady:"+prURL)
		pf.MarkReadyCalls = append(pf.MarkReadyCalls, prURL)
		if pf.MarkReadyErr != nil {
			return nil, pf.MarkReadyErr
		}
		pf.drafts[prURL] = false
		return nil, nil
	})
}

// CommentPRCall is one recorded CommentPR call.
type CommentPRCall struct {
	PR   string
	Body string
}

// CommentPR records the call to CommentPRCalls and returns CommentPRErr.
func (pf *PRForgeFake) CommentPR(prURL, body string) error {
	return pf.call(http.MethodPost, pathPRComment, nil, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		pf.CommentPRCalls = append(pf.CommentPRCalls, CommentPRCall{PR: prURL, Body: body})
		return nil, pf.CommentPRErr
	})
}

// MarkDraft records the call to MarkDraftCalls and, unless MarkDraftErr fails
// it, sets the PR's draft state: the inverse of MarkReady.
func (pf *PRForgeFake) MarkDraft(prURL string) error {
	return pf.call(http.MethodPatch, pathDraft, nil, func() (any, error) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		pf.LandingCallLog = append(pf.LandingCallLog, "MarkDraft:"+prURL)
		pf.MarkDraftCalls = append(pf.MarkDraftCalls, prURL)
		if pf.MarkDraftErr != nil {
			return nil, pf.MarkDraftErr
		}
		pf.drafts[prURL] = true
		return nil, nil
	})
}
