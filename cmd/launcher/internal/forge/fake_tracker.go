package forge

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
)

// IssueTrackerFake is the tracker-capability slice of Fake: every field
// IssueTracker and its optional LandingRecorder, IssueCloser, MergeCloser, and
// AbandonedFlagger interfaces read or write.
type IssueTrackerFake struct {
	*core

	labels DispatchLabels
	// VerdictLabels maps a Verdict to the label CompleteVerdict writes. Set it
	// directly: there is no constructor argument.
	VerdictLabels VerdictLabels
	issues        map[string]Issue
	// NativeDeps, keyed by issue number, is returned by DepsOf as
	// DepSourceNative and wins over body parsing when non-empty.
	// forgetest.RunTrackerContract pins that rule across every adapter.
	NativeDeps map[string][]string
	// NativeDepsErr, keyed by issue number, is returned by DepsOf instead of
	// consulting NativeDeps, scripting the native-API failure DepsOf falls
	// back to body parsing for (issue #1544).
	NativeDepsErr map[string]error

	// TouchesOfErr, keyed by issue number, is returned by TouchesOf instead of
	// parsing the body. Per-number, because one overlap-gate check calls
	// TouchesOf for both issues and a blanket error hides which side failed.
	TouchesOfErr map[string]error

	TransitionStateCalls []TransitionStateCall
	TransitionStateErr   error
	CompleteVerdictCalls []CompleteVerdictCall
	CompleteVerdictErr   error
	CommentCalls         []CommentCall
	CommentErr           error

	ListIssuesErr error
	// ListIssuesCalls records each call's state argument so a test can assert
	// the call count directly instead of inferring it (#987).
	ListIssuesCalls []DispatchState

	// IssueCalls records each call's issue number so a test can assert the
	// call count directly instead of inferring it (#1098).
	IssueCalls []string
	// IssueErr, if non-nil, is returned by every Issue call. ListOpenIssues
	// and ListIssues read the same issues map but never consult it, so a
	// body-fetch failure can be simulated independently (issue #1632).
	IssueErr error

	// DepsOfCalls records each call's issue number so a test can assert a
	// dependency-graph build's exact call count (issue #1632).
	DepsOfCalls []string

	Labels []string
	// LabelsSeq, when non-empty, is a per-call queue drained by ListLabels:
	// each call pops the first slice, and Labels is used once it is exhausted.
	LabelsSeq     [][]string
	ListLabelsErr error

	CreateLabelCalls []CreateLabelCall
	CreateLabelErr   error

	RecordLandingCalls []RecordLandingCall
	RecordLandingErr   error

	// RecordLandingPassCalls records each call in order (issue #2983).
	RecordLandingPassCalls []RecordLandingPassCall
	RecordLandingPassErr   error

	CloseIssueCalls []string
	CloseIssueErr   error

	// CloseMergedIssueCalls logs the optional MergeCloser calls separately
	// from CloseIssueCalls so a test can tell settle's post-merge backstop
	// apart from reconcile's closed: axis write (issue #1892).
	CloseMergedIssueCalls []string
	CloseMergedIssueErr   error

	FlagAbandonedCalls []string
	FlagAbandonedErr   error

	CommentsFor map[string][]Comment
	// CommentsErr, keyed by issue number, is returned by Comments instead of
	// consulting CommentsFor: the fetch failure IssueText degrades to
	// body-only for (issue #3445).
	CommentsErr map[string]error

	// PriorClaimStates, keyed by issue number, scripts the terminal label a
	// claim stripped immediately before. An absent key means not found
	// (ok=false), like a fresh dispatch carrying no prior terminal label.
	PriorClaimStates   map[string]DispatchState
	PriorClaimStateErr error
}

var _ IssueTracker = (*IssueTrackerFake)(nil)
var _ CommentLister = (*IssueTrackerFake)(nil)

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
		// Resolved from Labels at read time, not stored, so a test that sets
		// Labels without Priority cannot drift the two apart (#2281). The
		// github adapter resolves at its own read edge for the same reason.
		iss.Priority = ResolvePriority(iss.Labels)
		if label == "" {
			// A DispatchState the tracker's label family leaves unmapped
			// matches every open issue, not none: gh ignores `--label ""` and
			// Local's empty frontmatter state matches every untriaged issue.
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

// ListOpenIssues returns every non-closed issue, ascending by number, whatever
// its dispatch label.
func (tf *IssueTrackerFake) ListOpenIssues() ([]Issue, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	var out []Issue
	for _, iss := range tf.issues {
		if iss.State == IssueClosed {
			continue
		}
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

// allIssues returns every issue, open or closed, ascending by number. It stays
// lowercase so a bare *IssueTrackerFake never satisfies SeamLister; only
// seamListedIssueTracker promotes it to the exported AllIssues name.
func (tf *IssueTrackerFake) allIssues() ([]Issue, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	var out []Issue
	for _, iss := range tf.issues {
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
	iss.Priority = ResolvePriority(iss.Labels)
	return iss, nil
}

// TransitionState swaps the from-state label for the to-state label on issue
// num, best-effort on a missing issue (no error) to match the gh CLI. A claim
// (to == InProgress) also strips any stale Complete/Failed label left by a
// prior run, as the github adapter does, so a test on the Fake cannot pass
// while the real adapter misbehaves (#1985).
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
// issue num, best-effort on a missing issue like TransitionState. Unlike
// TransitionState it first asserts num carries InProgress and errors without
// mutating labels when it does not, the double-dispatch guard
// forgetest.RunTrackerContract pins across every adapter (#701).
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

// BlocksOf returns every issue number keyed in NativeDeps whose own deps name
// num as a blocker, the reverse of DepsOf, which the github and jira adapters
// can query because they store the relationship both ways (issue #1744). It is
// always DepSourceNative, sorted numerically only for deterministic assertions:
// NativeDeps is an unordered map, so a real adapter may order the set its way.
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

// TouchesOf returns the touch-set parsed from num's issue body, matching the
// real adapters' shared body-grammar default.
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

// Comments returns num's scripted CommentsFor entry. An unscripted number
// yields nil and no error, matching an issue with no comments.
func (tf *IssueTrackerFake) Comments(num string) ([]Comment, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	if err, ok := tf.CommentsErr[num]; ok {
		return nil, err
	}
	return tf.CommentsFor[num], nil
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

// StateLabels implements LabeledTracker, returning the DispatchLabels the Fake
// was constructed with.
func (tf *IssueTrackerFake) StateLabels() DispatchLabels {
	return tf.labels
}
