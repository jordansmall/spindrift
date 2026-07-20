package forge

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"sync"
)

// TransitionStateCall records a single TransitionState invocation.
type TransitionStateCall struct {
	Num      string
	From, To DispatchState
}

// CompleteVerdictCall records a single CompleteVerdict invocation.
type CompleteVerdictCall struct {
	Num     string
	Verdict Verdict
}

// CreateLabelCall records a single CreateLabel invocation.
type CreateLabelCall struct {
	Name, Description, Color string
}

// CommentCall records a single Comment invocation.
type CommentCall struct {
	Num, Body string
}

// RecordLandingCall records a single RecordLanding invocation.
type RecordLandingCall struct {
	Num, Landing string
}

// Fake is an in-memory Client for unit tests. All methods are safe for
// concurrent use. CheckState pops from a scripted RollupState queue so polling
// tests need no real sleeps.
type Fake struct {
	mu sync.Mutex

	labels DispatchLabels
	// VerdictLabels configures the Verdict-to-label mapping CompleteVerdict
	// uses, the same way labels configures TransitionState; set directly
	// (there is no constructor argument for it) since only research-kind
	// tests exercise it.
	VerdictLabels VerdictLabels
	issues        map[string]Issue
	// NativeDeps, when set for an issue number, is returned by DepsOf as
	// DepSourceNative and takes precedence over body parsing — the
	// native-wins-when-non-empty rule forgetest.RunTrackerContract's DepsOf
	// scenario pins across every adapter, so tests can script native-sourced,
	// body-sourced, and mixed-batch blockers.
	NativeDeps map[string][]string
	// NativeDepsErr, keyed by issue number, is returned by DepsOf for that
	// number instead of consulting NativeDeps — scripts the native-API
	// failure DepsOf falls back to body parsing for (forgetest's
	// NativeFailureIsolatable scenario, issue #1544).
	NativeDepsErr   map[string]error
	prs             map[string]PR             // URL → PR
	branchPRs       map[string]string         // branch → PR URL
	prStates        map[string]PRState        // URL → canonical PR state
	mergeableStates map[string]MergeableState // URL → scripted Mergeable result
	needsUpdate     map[string]bool           // URL → scripted NeedsUpdate result
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

	// OpenPRForBranchErr, if non-nil, is returned by every OpenPRForBranch
	// call (simulating a transient forge lookup failure, distinct from "no
	// open PR yet").
	OpenPRForBranchErr error

	// TouchesOfErr, keyed by issue number, is returned by TouchesOf for that
	// number instead of parsing its body. Per-number (not blanket, unlike
	// PRFilesErr) because a single overlap-gate check calls TouchesOf for
	// both an in-progress issue and the candidate being checked against it —
	// a blanket error couldn't isolate which side failed.
	TouchesOfErr map[string]error

	// MergeErr, if non-nil, is returned by every Merge call (after MergeErrs is drained).
	MergeErr error
	// NeedsUpdateErr, if non-nil, is returned by every NeedsUpdate call.
	NeedsUpdateErr error
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
	// CompleteVerdictCalls records all CompleteVerdict invocations in order.
	CompleteVerdictCalls []CompleteVerdictCall
	// CompleteVerdictErr, if non-nil, is returned by every CompleteVerdict call.
	CompleteVerdictErr error
	// CommentCalls records all Comment invocations in order.
	CommentCalls []CommentCall
	// RecordLandingCalls records all RecordLanding invocations in order.
	RecordLandingCalls []RecordLandingCall
	// RecordLandingErr, if non-nil, is returned by every RecordLanding call.
	RecordLandingErr error

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

	// LandingCallLog records, in order, every call to MarkReady, Merge, and
	// EnqueueAutoMerge as "Method:url" — the three landing-path methods a
	// caller can reorder relative to each other. A per-method Calls slice
	// alone can't distinguish "MarkReady then Merge" from "Merge then
	// MarkReady": both leave the same final Calls-slice contents, so a test
	// asserting call presence on each slice separately passes either way.
	// This single, cross-method log is what lets a test assert genuine
	// ordering (issue #1651's "ready-flip precedes the merge/enqueue call").
	LandingCallLog []string

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

	// ListIssuesErr, if non-nil, is returned by every ListIssues call.
	ListIssuesErr error
	// ListIssuesCalls records the state argument of every ListIssues
	// invocation in order — lets a test assert call count directly instead
	// of inferring it from side effects (#987).
	ListIssuesCalls []DispatchState

	// IssueCalls records the issue number argument of every Issue
	// invocation in order — lets a test assert call count directly instead
	// of inferring it from side effects (#1098).
	IssueCalls []string
	// IssueErr, if non-nil, is returned by every Issue call instead of the
	// looked-up issue — a blanket override (ListIssuesErr's own pattern),
	// letting a test simulate a body-fetch failure independently of
	// ListOpenIssues/ListIssues, which read the same issues map but never
	// consult this field (issue #1632).
	IssueErr error

	// DepsOfCalls records the issue number argument of every DepsOf
	// invocation in order — mirrors IssueCalls, letting a test assert a
	// dependency-graph build's exact call count (e.g. a whole-backlog
	// NewReadiness sweep) instead of inferring it from side effects
	// (issue #1632).
	DepsOfCalls []string
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
		needsUpdate:     map[string]bool{},
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

func (f *Fake) ListIssues(state DispatchState) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ListIssuesCalls = append(f.ListIssuesCalls, state)
	if f.ListIssuesErr != nil {
		return nil, f.ListIssuesErr
	}
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

// ListOpenIssues returns every non-closed issue regardless of dispatch
// label, ascending by number — mirroring ListIssues' canonical order.
func (f *Fake) ListOpenIssues() ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Issue
	for _, iss := range f.issues {
		if iss.State == IssueClosed {
			continue
		}
		out = append(out, iss)
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
	f.IssueCalls = append(f.IssueCalls, num)
	if f.IssueErr != nil {
		return Issue{}, f.IssueErr
	}
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

// CompleteVerdict swaps the InProgress label for verdict's terminal label on
// issue num. Best-effort on missing issues (no error), matching
// TransitionState's contract. Unlike TransitionState, it asserts num
// currently carries InProgress before editing — the double-dispatch guard
// (#701) forgetest.RunTrackerContract's DoubleDispatchGuard scenario pins
// across every adapter — and errors without mutating labels when it's
// absent.
func (f *Fake) CompleteVerdict(num string, verdict Verdict) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CompleteVerdictCalls = append(f.CompleteVerdictCalls, CompleteVerdictCall{num, verdict})
	if f.CompleteVerdictErr != nil {
		return f.CompleteVerdictErr
	}
	iss, ok := f.issues[num]
	if !ok {
		return nil // best-effort
	}
	add := f.VerdictLabels.Label(verdict)
	if add == "" {
		return fmt.Errorf("issue %s: no label configured for verdict %v", num, verdict)
	}
	remove := f.labels.Label(InProgress)
	if remove != "" && !slices.Contains(iss.Labels, remove) {
		return fmt.Errorf("issue %s: expected %q label, issue has %v", num, remove, iss.Labels)
	}
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

// DepsOf returns num's scripted NativeDeps (DepSourceNative) when set,
// otherwise the dependency IDs parsed from the issue body (DepSourceBody).
func (f *Fake) DepsOf(num string) ([]Dependency, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DepsOfCalls = append(f.DepsOfCalls, num)
	if err := f.NativeDepsErr[num]; err == nil {
		if native, ok := f.NativeDeps[num]; ok && len(native) > 0 {
			return WithSource(native, DepSourceNative), nil
		}
	}
	iss, ok := f.issues[num]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", num)
	}
	return WithSource(ParseBlockerRefs(iss.Body), DepSourceBody), nil
}

// TouchesOf returns the touch-set parsed from num's issue body, mirroring
// the real adapters' shared body-grammar default.
func (f *Fake) TouchesOf(num string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.TouchesOfErr[num]; ok {
		return nil, err
	}
	iss, ok := f.issues[num]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", num)
	}
	return ParseTouchPaths(iss.Body), nil
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
	if f.OpenPRForBranchErr != nil {
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

// RecordLanding implements the optional LandingRecorder surface (ADR 0029),
// recording each call for tests to assert against.
func (f *Fake) RecordLanding(num, landing string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RecordLandingCalls = append(f.RecordLandingCalls, RecordLandingCall{num, landing})
	return f.RecordLandingErr
}

var _ LandingRecorder = (*Fake)(nil)

// noLandingIssueTracker adapts a Fake to expose only the core IssueTracker
// surface, hiding its RecordLanding method so a type assertion against it
// reports absence — the IssueTracker analogue of pushOnlyForge, matching a
// github/jira adapter's shape (ADR 0029).
type noLandingIssueTracker struct{ IssueTracker }

// AsNoLandingRecorder returns f wrapped so it satisfies IssueTracker but not
// LandingRecorder.
func (f *Fake) AsNoLandingRecorder() IssueTracker { return noLandingIssueTracker{f} }

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
