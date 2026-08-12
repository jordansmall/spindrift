package forge

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
)

// IssueTrackerFake is the tracker-capability slice of Fake, holding every
// field the IssueTracker surface (plus its optional LandingRecorder,
// IssueCloser, MergeCloser, and AbandonedFlagger surfaces) reads or writes.
// It embeds *core — see core's doc comment for the admission rule — so its
// methods can reach mu/prStates/etc. directly.
type IssueTrackerFake struct {
	// *core is the shared substrate promoted through to IssueTrackerFake —
	// see core's doc comment for the admission rule.
	*core

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
	NativeDepsErr map[string]error

	// TouchesOfErr, keyed by issue number, is returned by TouchesOf for that
	// number instead of parsing its body. Per-number (not blanket, unlike
	// PRFilesErr) because a single overlap-gate check calls TouchesOf for
	// both an in-progress issue and the candidate being checked against it —
	// a blanket error couldn't isolate which side failed.
	TouchesOfErr map[string]error

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
	// CommentErr, if non-nil, is returned by every Comment call.
	CommentErr error

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

	// RecordLandingCalls records all RecordLanding invocations in order.
	RecordLandingCalls []RecordLandingCall
	// RecordLandingErr, if non-nil, is returned by every RecordLanding call.
	RecordLandingErr error

	// CloseIssueCalls records the issue number argument of every CloseIssue
	// invocation in order.
	CloseIssueCalls []string
	// CloseIssueErr, if non-nil, is returned by every CloseIssue call.
	CloseIssueErr error

	// CloseMergedIssueCalls records the issue number argument of every
	// CloseMergedIssue invocation in order — the optional MergeCloser
	// surface's own call log (issue #1892), kept separate from
	// CloseIssueCalls so a test can tell settle's post-merge backstop apart
	// from reconcile's closed: axis write.
	CloseMergedIssueCalls []string
	// CloseMergedIssueErr, if non-nil, is returned by every CloseMergedIssue
	// call.
	CloseMergedIssueErr error

	// FlagAbandonedCalls records the issue number argument of every
	// FlagAbandoned invocation in order.
	FlagAbandonedCalls []string
	// FlagAbandonedErr, if non-nil, is returned by every FlagAbandoned call.
	FlagAbandonedErr error

	// PriorClaimStates, keyed by issue number, scripts what a real tracker's
	// optional PriorClaimStateReader surface would read back from the issue's
	// timeline for the terminal label a claim stripped immediately before —
	// unset (key absent) means "not found" (ok=false), matching a fresh
	// dispatch that carries no prior terminal label at all.
	PriorClaimStates map[string]DispatchState
	// PriorClaimStateErr, if non-nil, is returned by every PriorClaimState call.
	PriorClaimStateErr error
}

var _ IssueTracker = (*IssueTrackerFake)(nil)

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

func (tf *IssueTrackerFake) ListIssues(state DispatchState) ([]Issue, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.ListIssuesCalls = append(tf.ListIssuesCalls, state)
	if tf.ListIssuesErr != nil {
		return nil, tf.ListIssuesErr
	}
	label := tf.labels.Label(state)
	var out []Issue
	for _, iss := range tf.issues {
		if iss.State == IssueClosed {
			continue
		}
		// Resolved from Labels at read time, not stored separately, so
		// Labels stays the single source of truth (#2281): a test that sets
		// Labels via SetIssue without also setting Priority can't drift the
		// two out of sync, mirroring the github adapter's resolution at its
		// own read edge (exec_issues.go).
		iss.Priority = ResolvePriority(iss.Labels)
		if label == "" {
			// Mirrors GitHub's `--label ""` (ignored by gh, returns every
			// open issue) and Local's `frontmatter.State == ""` (matches
			// every untriaged issue): a DispatchState left unmapped by the
			// tracker's label family matches everything, not nothing.
			out = append(out, iss)
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
func (tf *IssueTrackerFake) ListOpenIssues() ([]Issue, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	var out []Issue
	for _, iss := range tf.issues {
		if iss.State == IssueClosed {
			continue
		}
		// See ListIssues's matching comment: resolved from Labels at read
		// time so Labels stays the single source of truth.
		iss.Priority = ResolvePriority(iss.Labels)
		out = append(out, iss)
	}
	sort.Slice(out, func(i, j int) bool {
		ni, _ := strconv.Atoi(out[i].Number)
		nj, _ := strconv.Atoi(out[j].Number)
		return ni < nj
	})
	return out, nil
}

func (tf *IssueTrackerFake) Issue(num string) (Issue, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.IssueCalls = append(tf.IssueCalls, num)
	if tf.IssueErr != nil {
		return Issue{}, tf.IssueErr
	}
	iss, ok := tf.issues[num]
	if !ok {
		return Issue{}, fmt.Errorf("issue %s not found", num)
	}
	// See ListIssues's matching comment: resolved from Labels at read time
	// so Labels stays the single source of truth.
	iss.Priority = ResolvePriority(iss.Labels)
	return iss, nil
}

// TransitionState swaps the from-state label for the to-state label on issue
// num. Best-effort on missing issues (no error), matching gh CLI behavior. A
// claim (to == InProgress) also strips any stale Complete/Failed terminal
// label the issue still carries from a prior run, mirroring the github
// adapter's TransitionState (exec_issues.go) so a launcher-level test built
// on the Fake can't pass while the real adapter still misbehaves (#1985).
func (tf *IssueTrackerFake) TransitionState(num string, from, to DispatchState) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.TransitionStateCalls = append(tf.TransitionStateCalls, TransitionStateCall{num, from, to})
	if tf.TransitionStateErr != nil {
		return tf.TransitionStateErr
	}
	iss, ok := tf.issues[num]
	if !ok {
		return nil // best-effort
	}
	add := tf.labels.Label(to)
	remove := map[string]bool{}
	for _, l := range tf.labels.ClaimRemoveLabels(from, to) {
		remove[l] = true
	}
	var next []string
	for _, l := range iss.Labels {
		if !remove[l] {
			next = append(next, l)
		}
	}
	next = append(next, add)
	iss.Labels = next
	tf.issues[num] = iss
	return nil
}

// CompleteVerdict swaps the InProgress label for verdict's terminal label on
// issue num. Best-effort on missing issues (no error), matching
// TransitionState's contract. Unlike TransitionState, it asserts num
// currently carries InProgress before editing — the double-dispatch guard
// (#701) forgetest.RunTrackerContract's DoubleDispatchGuard scenario pins
// across every adapter — and errors without mutating labels when it's
// absent.
func (tf *IssueTrackerFake) CompleteVerdict(num string, verdict Verdict) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.CompleteVerdictCalls = append(tf.CompleteVerdictCalls, CompleteVerdictCall{num, verdict})
	if tf.CompleteVerdictErr != nil {
		return tf.CompleteVerdictErr
	}
	iss, ok := tf.issues[num]
	if !ok {
		return nil // best-effort
	}
	add := tf.VerdictLabels.Label(verdict)
	if add == "" {
		return fmt.Errorf("issue %s: no label configured for verdict %v", num, verdict)
	}
	remove := tf.labels.Label(InProgress)
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
	tf.issues[num] = iss
	return nil
}

// DepsOf returns num's scripted NativeDeps (DepSourceNative) when set,
// otherwise the dependency IDs parsed from the issue body (DepSourceBody).
func (tf *IssueTrackerFake) DepsOf(num string) ([]Dependency, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.DepsOfCalls = append(tf.DepsOfCalls, num)
	if err := tf.NativeDepsErr[num]; err == nil {
		if native, ok := tf.NativeDeps[num]; ok && len(native) > 0 {
			return WithSource(native, DepSourceNative), nil
		}
	}
	iss, ok := tf.issues[num]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", num)
	}
	return WithSource(ParseBlockerRefs(iss.Body), DepSourceBody), nil
}

// BlocksOf returns every issue number keyed in NativeDeps whose own deps
// name num as a blocker — DepsOf's reverse direction, mirroring the real
// github/jira adapters' native issue-dependencies relationship, which is
// stored (and so queryable) in both directions (issue #1744). Always
// DepSourceNative: NativeDeps has no body-sourced counterpart to reverse.
// Sorted ascending by numeric value for deterministic test assertions —
// unlike DepsOf, which preserves API response order and makes no ordering
// promise of its own, NativeDeps is an unordered map with no natural
// "response order" to preserve, so a real github/jira adapter's own
// BlocksOf may legitimately return the same set in a different order.
func (tf *IssueTrackerFake) BlocksOf(num string) ([]Dependency, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	var ids []string
	for child, blockers := range tf.NativeDeps {
		if slices.Contains(blockers, num) {
			ids = append(ids, child)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		ni, ei := strconv.Atoi(ids[i])
		nj, ej := strconv.Atoi(ids[j])
		if ei == nil && ej == nil {
			return ni < nj
		}
		return ids[i] < ids[j]
	})
	return WithSource(ids, DepSourceNative), nil
}

// TouchesOf returns the touch-set parsed from num's issue body, mirroring
// the real adapters' shared body-grammar default.
func (tf *IssueTrackerFake) TouchesOf(num string) ([]string, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	if err, ok := tf.TouchesOfErr[num]; ok {
		return nil, err
	}
	iss, ok := tf.issues[num]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", num)
	}
	return ParseTouchPaths(iss.Body), nil
}

func (tf *IssueTrackerFake) Comment(num, body string) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.CommentCalls = append(tf.CommentCalls, CommentCall{num, body})
	return tf.CommentErr
}

func (tf *IssueTrackerFake) ListLabels() ([]string, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	if tf.ListLabelsErr != nil {
		return nil, tf.ListLabelsErr
	}
	src := tf.Labels
	if len(tf.LabelsSeq) > 0 {
		src = tf.LabelsSeq[0]
		tf.LabelsSeq = tf.LabelsSeq[1:]
	}
	out := make([]string, len(src))
	copy(out, src)
	return out, nil
}

func (tf *IssueTrackerFake) CreateLabel(name, description, color string) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.CreateLabelCalls = append(tf.CreateLabelCalls, CreateLabelCall{name, description, color})
	return tf.CreateLabelErr
}

// SetIssue upserts an issue into the fake store.
func (tf *IssueTrackerFake) SetIssue(iss Issue) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.issues[iss.Number] = iss
}

func (tf *IssueTrackerFake) Probe() (string, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	if tf.ProbeErr != nil {
		return "", tf.ProbeErr
	}
	return tf.ProbeRepo, nil
}

// StateLabels implements LabeledTracker, returning the DispatchLabels the
// Fake was constructed with.
func (tf *IssueTrackerFake) StateLabels() DispatchLabels {
	return tf.labels
}
