package forge

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
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
	// NativeDeps, keyed by issue number, is returned by NativeDepsOf and wins
	// over body parsing in DepsOf when non-empty.
	// forgetest.RunTrackerContract pins that rule across every adapter.
	NativeDeps map[string][]string
	// NativeDepsErr, keyed by issue number, is returned by NativeDepsOf instead
	// of consulting NativeDeps, scripting the native-API failure DepsOf falls
	// back to body parsing for (issue #1544).
	NativeDepsErr map[string]error

	// IssueErrByNum, keyed by issue number, is returned by Issue for that
	// number alone, scripting a failed raw issue read. Per-number, because one
	// overlap-gate check reads both issues and a blanket IssueErr hides which
	// side failed.
	IssueErrByNum map[string]error

	TransitionStateCalls []TransitionStateCall
	TransitionStateErr   error
	// TransitionStateErrByNum, keyed by issue number, fails TransitionState for
	// that issue alone, ahead of TransitionStateErr.
	TransitionStateErrByNum map[string]error
	CompleteVerdictCalls    []CompleteVerdictCall
	CompleteVerdictErr      error
	CommentCalls            []CommentCall
	CommentErr              error

	ListIssuesErr error
	// ListIssuesCalls records each call's state argument so a test can assert
	// the call count directly instead of inferring it (#987).
	ListIssuesCalls []DispatchState

	// IssueCalls records each call's issue number so a test can assert the
	// call count directly instead of inferring it (#1098).
	IssueCalls []string
	// IssueErr, if non-nil, is returned by every Issue call once IssueErrs is
	// drained. ListOpenIssues and ListIssues read the same issues map but never
	// consult it, so a body-fetch failure can be simulated independently (issue
	// #1632).
	IssueErr error

	// IssueErrs is a per-call queue drained before IssueErr is checked. A nil
	// entry falls through to the normal issue lookup. forge.DepsOf and
	// forge.TouchesOf read through Issue, so they record IssueCalls and drain
	// both IssueErrs and IssueErr too.
	IssueErrs []error

	// NativeDepsCalls records each NativeDepsOf call's issue number, the native
	// read core's DepsOf makes, so a test can assert a dependency-graph build's
	// exact call count (issue #1632).
	NativeDepsCalls []string

	Labels []string
	// LabelsSeq, when non-empty, is a per-call queue drained by ListLabels:
	// each call pops the first slice, and Labels is used once it is exhausted.
	LabelsSeq     [][]string
	ListLabelsErr error

	CreateLabelCalls []CreateLabelCall
	CreateLabelErr   error
	// RequireLabelsExist makes the filer's PostIssue fail on any label not in
	// Labels, as GitHub's `gh issue create --label` does (issue #4400).
	RequireLabelsExist bool

	// AddLabelsCalls records each AddLabels invocation so a test can assert
	// call count and arguments directly (issue #4074).
	AddLabelsCalls []AddLabelsCall
	AddLabelsErr   error

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
var _ IssueLabeler = (*IssueTrackerFake)(nil)

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

// AddLabelsCall records a single AddLabels invocation.
type AddLabelsCall struct {
	Num    string
	Labels []string
}

// CommentCall records a single Comment invocation.
type CommentCall struct {
	Num, Body string
}

func (tf *IssueTrackerFake) ListIssues(state DispatchState) ([]Issue, error) {
	var out []Issue
	err := tf.read(pathListIssues, &out, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		tf.ListIssuesCalls = append(tf.ListIssuesCalls, state)
		if tf.ListIssuesErr != nil {
			return nil, tf.ListIssuesErr
		}
		return tf.listState(state), nil
	})
	return out, err
}

// CountReady implements DemandCounter. It shares ListIssuesErr with
// ListIssues (a failing tracker fails both) but, unlike ListIssues, is not
// recorded in ListIssuesCalls, so a demand probe never perturbs a test that
// asserts which states the dispatch loop listed.
func (tf *IssueTrackerFake) CountReady(_ bool) (int, error) {
	var n int
	err := tf.read(pathListIssues, &n, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		if tf.ListIssuesErr != nil {
			return nil, tf.ListIssuesErr
		}
		return len(tf.listState(Dispatchable)), nil
	})
	return n, err
}

// ProbeInterval implements DemandCounter.
func (tf *IssueTrackerFake) ProbeInterval() time.Duration { return time.Second }

// listState returns the open issues in state, ascending by number; the
// caller holds tf.mu.
func (tf *IssueTrackerFake) listState(state DispatchState) []Issue {
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
	return out
}

// ListOpenIssues returns every non-closed issue, ascending by number, whatever
// its dispatch label.
func (tf *IssueTrackerFake) ListOpenIssues() ([]Issue, error) {
	var out []Issue
	err := tf.read(pathListIssues, &out, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		var open []Issue
		for _, iss := range tf.issues {
			if iss.State == IssueClosed {
				continue
			}
			iss.Priority = ResolvePriority(iss.Labels)
			open = append(open, iss)
		}
		sort.Slice(open, func(i, j int) bool {
			ni, _ := strconv.Atoi(open[i].Number)
			nj, _ := strconv.Atoi(open[j].Number)
			return ni < nj
		})
		return open, nil
	})
	return out, err
}

// allIssues returns every issue, open or closed, ascending by number. It stays
// lowercase so a bare *IssueTrackerFake never satisfies SeamLister; only
// seamListedIssueTracker promotes it to the exported AllIssues name.
func (tf *IssueTrackerFake) allIssues() ([]Issue, error) {
	var out []Issue
	err := tf.read(pathAllIssues, &out, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		var all []Issue
		for _, iss := range tf.issues {
			iss.Priority = ResolvePriority(iss.Labels)
			all = append(all, iss)
		}
		sort.Slice(all, func(i, j int) bool {
			ni, _ := strconv.Atoi(all[i].Number)
			nj, _ := strconv.Atoi(all[j].Number)
			return ni < nj
		})
		return all, nil
	})
	return out, err
}

func (tf *IssueTrackerFake) Issue(num string) (Issue, error) {
	var out Issue
	err := tf.read(pathIssue, &out, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		tf.IssueCalls = append(tf.IssueCalls, num)
		if err, ok := tf.IssueErrByNum[num]; ok {
			return nil, err
		}
		if err := popErr(&tf.IssueErrs, tf.IssueErr); err != nil {
			return nil, err
		}
		iss, ok := tf.issues[num]
		if !ok {
			return nil, fmt.Errorf("issue %s not found", num)
		}
		iss.Priority = ResolvePriority(iss.Labels)
		return iss, nil
	})
	return out, err
}

// TransitionState swaps the from-state label for the to-state label on issue
// num, best-effort on a missing issue (no error) to match the gh CLI. A claim
// (to == InProgress) also strips the stale terminal labels
// TransitionRemoveLabels names, as the github adapter does, so a test on the
// Fake cannot pass while the real adapter misbehaves (#1985). A claim also
// strips any research verdict label. A landing (to == Complete) also strips a
// stale Failed, as the adapters do (#4651).
//
// A claim also errors on ErrAlreadyClaimed without mutating labels when num
// already carries InProgress (#3887), so a test on the Fake pins the same
// contract the real adapters must honor.
func (tf *IssueTrackerFake) TransitionState(num string, from, to DispatchState) error {
	tf.Fire("TransitionState")
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.TransitionStateCalls = append(tf.TransitionStateCalls, TransitionStateCall{num, from, to})
	if err := tf.TransitionStateErrByNum[num]; err != nil {
		return err
	}
	if tf.TransitionStateErr != nil {
		return tf.TransitionStateErr
	}
	iss, ok := tf.issues[num]
	if !ok {
		return nil // best-effort
	}
	if tf.labels.AlreadyClaimed(from, to, iss.Labels) {
		return fmt.Errorf("fake: issue %s: %w (%q)", num, ErrAlreadyClaimed, tf.labels.Label(to))
	}
	add := tf.labels.Label(to)
	remove := map[string]bool{}
	for _, l := range TransitionRemoveLabels(tf.labels, tf.VerdictLabels, from, to) {
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

var _ NativeDepsReader = (*IssueTrackerFake)(nil)

// NativeDepsOf returns num's scripted NativeDeps, or NativeDepsErr[num] when
// set. It records num in NativeDepsCalls, the spy for a dependency-graph build
// (forge.DepsOf calls it once per issue).
func (tf *IssueTrackerFake) NativeDepsOf(num string) ([]string, error) {
	var out []string
	err := tf.read(pathNativeDeps, &out, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		tf.NativeDepsCalls = append(tf.NativeDepsCalls, num)
		if err := tf.NativeDepsErr[num]; err != nil {
			return nil, err
		}
		return tf.NativeDeps[num], nil
	})
	return out, err
}

// BlocksOf returns every issue number keyed in NativeDeps whose own deps name
// num as a blocker, the reverse of DepsOf, which every BlockersLister adapter
// can query because it stores the relationship both ways (issue #1744). It
// is always DepSourceNative, sorted numerically only for
// deterministic assertions: NativeDeps is an unordered map, so a real adapter
// may order the set its way.
func (tf *IssueTrackerFake) BlocksOf(num string) ([]Dependency, error) {
	var out []Dependency
	err := tf.read(pathBlocks, &out, func() (any, error) {
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
	})
	return out, err
}

func (tf *IssueTrackerFake) Comment(num, body string) error {
	tf.Fire("Comment")
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.CommentCalls = append(tf.CommentCalls, CommentCall{num, body})
	return tf.CommentErr
}

// Comments returns num's scripted CommentsFor entry. An unscripted number
// yields nil and no error, matching an issue with no comments.
func (tf *IssueTrackerFake) Comments(num string) ([]Comment, error) {
	var out []Comment
	err := tf.read(pathComments, &out, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		if err, ok := tf.CommentsErr[num]; ok {
			return nil, err
		}
		return tf.CommentsFor[num], nil
	})
	return out, err
}

func (tf *IssueTrackerFake) ListLabels() ([]string, error) {
	var out []string
	err := tf.read(pathLabels, &out, func() (any, error) {
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
		return append([]string{}, src...), nil
	})
	return out, err
}

func (tf *IssueTrackerFake) CreateLabel(name, description, color string) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.CreateLabelCalls = append(tf.CreateLabelCalls, CreateLabelCall{name, description, color})
	if tf.CreateLabelErr == nil {
		tf.Labels = append(tf.Labels, name)
	}
	return tf.CreateLabelErr
}

// missingLabels returns the labels a RequireLabelsExist PostIssue rejects.
func (tf *IssueTrackerFake) missingLabels(labels []string) []string {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	if !tf.RequireLabelsExist {
		return nil
	}
	var out []string
	for _, l := range labels {
		if !slices.Contains(tf.Labels, l) {
			out = append(out, l)
		}
	}
	return out
}

// AddLabels implements the optional IssueLabeler interface (issue #4074). It
// errors on an unknown num, unlike TransitionState's best-effort no-op: a
// caller reaching for AddLabels always names an issue it just filed or
// otherwise knows exists, so a missing issue is a real fake-setup bug, not a
// benign double-dispatch the way a stale TransitionState can be.
func (tf *IssueTrackerFake) AddLabels(num string, labels []string) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.AddLabelsCalls = append(tf.AddLabelsCalls, AddLabelsCall{Num: num, Labels: append([]string(nil), labels...)})
	if tf.AddLabelsErr != nil {
		return tf.AddLabelsErr
	}
	iss, ok := tf.issues[num]
	if !ok {
		return fmt.Errorf("issue %s not found", num)
	}
	for _, l := range labels {
		if !slices.Contains(iss.Labels, l) {
			iss.Labels = append(iss.Labels, l)
		}
	}
	tf.issues[num] = iss
	return nil
}

// SetIssue upserts an issue into the fake store.
func (tf *IssueTrackerFake) SetIssue(iss Issue) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.issues[iss.Number] = iss
}

func (tf *IssueTrackerFake) Probe() (string, error) { return tf.probe(pathTrackerProbe) }

// StateLabels implements LabeledTracker, returning the DispatchLabels the Fake
// was constructed with.
func (tf *IssueTrackerFake) StateLabels() DispatchLabels {
	return tf.labels
}

var _ IssueSearcher = (*IssueTrackerFake)(nil)

// SearchIssues filters the fake store by state (an unset stored State counts
// as open; a searched state other than IssueOpen or IssueClosed is an error),
// exact label, and case-insensitive substring of every term in title or body,
// newest (highest number) first, capped at limit (a negative limit is zero, as
// in the Connector path).
func (tf *IssueTrackerFake) SearchIssues(text string, state IssueState, label string, limit int) ([]Issue, error) {
	var out []Issue
	// Reads through pathListIssues on purpose: an error injected for
	// list-issues also fails search. The state check sits inside the read so a
	// Connector-backed fake still crosses the Connector first.
	err := tf.read(pathListIssues, &out, func() (any, error) {
		if state != IssueOpen && state != IssueClosed {
			return nil, fmt.Errorf("fake tracker: unsupported issue state %q", state)
		}
		return tf.searchIssues(text, state, label, limit), nil
	})
	return out, err
}

func (tf *IssueTrackerFake) searchIssues(text string, state IssueState, label string, limit int) []Issue {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	terms := strings.Fields(strings.ToLower(text))
	var out []Issue
	for _, iss := range tf.issues {
		if (iss.State == IssueClosed) != (state == IssueClosed) {
			continue
		}
		if label != "" && !slices.Contains(iss.Labels, label) {
			continue
		}
		hay := strings.ToLower(iss.Title + "\n" + iss.Body)
		if !slices.ContainsFunc(terms, func(t string) bool { return !strings.Contains(hay, t) }) {
			iss.Priority = ResolvePriority(iss.Labels)
			out = append(out, iss)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ni, _ := strconv.Atoi(out[i].Number)
		nj, _ := strconv.Atoi(out[j].Number)
		return ni > nj
	})
	if limit = max(limit, 0); len(out) > limit {
		out = out[:limit]
	}
	return out
}
