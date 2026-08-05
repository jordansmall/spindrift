package forge

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
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
func (f *Fake) ListOpenIssues() ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Issue
	for _, iss := range f.issues {
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
	remove := map[string]bool{}
	for _, l := range f.labels.ClaimRemoveLabels(from, to) {
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
func (f *Fake) BlocksOf(num string) ([]Dependency, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for child, blockers := range f.NativeDeps {
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
	return f.CommentErr
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
