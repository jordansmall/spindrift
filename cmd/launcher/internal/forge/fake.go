package forge

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
)

// TransitionStateCall records a single TransitionState invocation.
type TransitionStateCall struct {
	Num      string
	From, To DispatchState
}

// CreateLabelCall records a single CreateLabel invocation.
type CreateLabelCall struct {
	Name, Description, Color string
}

// CommentCall records a single Comment invocation.
type CommentCall struct {
	Num, Body string
}

// Fake is an in-memory Client for unit tests. All methods are safe for
// concurrent use. CheckState pops from a scripted RollupState queue so polling
// tests need no real sleeps.
type Fake struct {
	mu sync.Mutex

	labels          DispatchLabels
	issues          map[string]Issue
	prs             map[string]PR             // URL → PR
	branchPRs       map[string]string         // branch → PR URL
	prStates        map[string]PRState        // URL → canonical PR state
	mergeableStates map[string]MergeableState // URL → scripted Mergeable result
	checkQ          map[string][]RollupState
	checkErrQ       map[string][]error  // per-call error queue; nil entry = consult checkQ
	prFiles         map[string][]string // URL → scripted ListPRFiles result

	failureDetail map[string]string // URL → scripted FailureDetail result
	// FailureDetailErr, if non-nil, is returned by every FailureDetail call.
	FailureDetailErr error

	// PRStateErr, if non-nil, is returned by every PRState call (simulating a
	// push-only Code Forge, where PR state has no meaning).
	PRStateErr error

	// PRFilesErr, if non-nil, is returned by every ListPRFiles call.
	PRFilesErr error

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
	// TransitionStateCalls records all TransitionState invocations in order.
	TransitionStateCalls []TransitionStateCall
	// TransitionStateErr, if non-nil, is returned by every TransitionState call.
	TransitionStateErr error
	// CommentCalls records all Comment invocations in order.
	CommentCalls []CommentCall

	// AutoMergeAllowed controls what CanAutoMerge returns (default false).
	AutoMergeAllowed bool
	// AutoMergeErr, if non-nil, is returned by CanAutoMerge.
	AutoMergeErr error
	// EnqueueAutoMergeErr, if non-nil, is returned by EnqueueAutoMerge.
	EnqueueAutoMergeErr error
	// EnqueueAutoMergeCalls records all PR URLs passed to EnqueueAutoMerge.
	EnqueueAutoMergeCalls []string

	// ProbeErr, if non-nil, is returned by Probe. Use ErrAuthFailure or
	// ErrRepoNotFound to simulate specific failure modes.
	ProbeErr error
	// ProbeRepo is the resolved repo slug returned by Probe on success.
	ProbeRepo string

	// Labels is the list of label names returned by ListLabels on success.
	// When LabelsSeq is non-empty, each call pops the next entry from it
	// instead (falling back to Labels once the sequence is exhausted).
	Labels []string
	// LabelsSeq, when non-empty, is a per-call queue drained by ListLabels.
	// Each call pops the first slice; when exhausted, Labels is used.
	LabelsSeq [][]string
	// ListLabelsErr, if non-nil, is returned by ListLabels.
	ListLabelsErr error

	// CreateLabelCalls records all CreateLabel invocations in order.
	CreateLabelCalls []CreateLabelCall
	// CreateLabelErr, if non-nil, is returned by every CreateLabel call.
	CreateLabelErr error

	// BranchPrefix is baked into AgentBranch's output. Zero value "" matches
	// an unconfigured config.branchPrefix; set explicitly to exercise a real
	// prefix (e.g. "agent/issue-").
	BranchPrefix string
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
	return &Fake{
		labels:          l,
		issues:          map[string]Issue{},
		prs:             map[string]PR{},
		branchPRs:       map[string]string{},
		prStates:        map[string]PRState{},
		mergeableStates: map[string]MergeableState{},
		checkQ:          map[string][]RollupState{},
		checkErrQ:       map[string][]error{},
		prFiles:         map[string][]string{},

		failureDetail: map[string]string{},
	}
}

// AgentBranch returns BranchPrefix + num.
func (f *Fake) AgentBranch(num string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.BranchPrefix + num
}

// SetIssue upserts an issue into the fake store.
func (f *Fake) SetIssue(iss Issue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues[iss.Number] = iss
}

// SetPR registers a PR reachable by the given head branch name.
func (f *Fake) SetPR(branch string, pr PR) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs[pr.URL] = pr
	f.branchPRs[branch] = pr.URL
	if _, ok := f.prStates[pr.URL]; !ok {
		f.prStates[pr.URL] = PROpen
	}
}

// SetPRState overrides the canonical state of a known PR.
func (f *Fake) SetPRState(url string, state PRState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prStates[url] = state
}

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

func (f *Fake) ListIssues(state DispatchState) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	label := f.labels.Label(state)
	var out []Issue
	for _, iss := range f.issues {
		if iss.State == IssueClosed {
			continue
		}
		for _, l := range iss.Labels {
			if l == label {
				out = append(out, iss)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ni, _ := strconv.Atoi(out[i].Number)
		nj, _ := strconv.Atoi(out[j].Number)
		return ni < nj
	})
	return out, nil
}

func (f *Fake) Issue(num string) (Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	iss, ok := f.issues[num]
	if !ok {
		return Issue{}, fmt.Errorf("issue %s not found", num)
	}
	return iss, nil
}

// TransitionState swaps the from-state label for the to-state label on issue
// num. Best-effort on missing issues (no error), matching gh CLI behavior.
func (f *Fake) TransitionState(num string, from, to DispatchState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TransitionStateCalls = append(f.TransitionStateCalls, TransitionStateCall{num, from, to})
	if f.TransitionStateErr != nil {
		return f.TransitionStateErr
	}
	iss, ok := f.issues[num]
	if !ok {
		return nil // best-effort
	}
	add := f.labels.Label(to)
	remove := f.labels.Label(from)
	var next []string
	for _, l := range iss.Labels {
		if l != remove {
			next = append(next, l)
		}
	}
	next = append(next, add)
	iss.Labels = next
	f.issues[num] = iss
	return nil
}

// DepsOf returns the dependency IDs parsed from the issue body.
func (f *Fake) DepsOf(num string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	iss, ok := f.issues[num]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", num)
	}
	return ParseBlockerRefs(iss.Body), nil
}

func (f *Fake) Comment(num, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CommentCalls = append(f.CommentCalls, CommentCall{num, body})
	return nil
}

func (f *Fake) OpenPRForBranch(branch string) (PR, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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

func (f *Fake) Merge(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.EnqueueAutoMergeCalls = append(f.EnqueueAutoMergeCalls, prURL)
	return f.EnqueueAutoMergeErr
}

func (f *Fake) Probe() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ProbeErr != nil {
		return "", f.ProbeErr
	}
	return f.ProbeRepo, nil
}

func (f *Fake) ListLabels() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListLabelsErr != nil {
		return nil, f.ListLabelsErr
	}
	src := f.Labels
	if len(f.LabelsSeq) > 0 {
		src = f.LabelsSeq[0]
		f.LabelsSeq = f.LabelsSeq[1:]
	}
	out := make([]string, len(src))
	copy(out, src)
	return out, nil
}

func (f *Fake) CreateLabel(name, description, color string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CreateLabelCalls = append(f.CreateLabelCalls, CreateLabelCall{name, description, color})
	return f.CreateLabelErr
}

// pushOnlyForge adapts a Fake to expose only the core CodeForge surface,
// hiding its PRForge methods so a type assertion against it reports absence
// — the git adapter's shape, for tests that need to exercise push-only-forge
// behavior without a removed PushOnly() flag.
type pushOnlyForge struct{ f *Fake }

// AsPushOnly returns f wrapped so it satisfies CodeForge but not PRForge.
func (f *Fake) AsPushOnly() CodeForge { return pushOnlyForge{f} }

func (p pushOnlyForge) AgentBranch(num string) string { return p.f.AgentBranch(num) }
func (p pushOnlyForge) Merge(url string) error        { return p.f.Merge(url) }
func (p pushOnlyForge) Rebase(url string) error       { return p.f.Rebase(url) }
func (p pushOnlyForge) Probe() (string, error)        { return p.f.Probe() }

var _ CodeForge = pushOnlyForge{}
