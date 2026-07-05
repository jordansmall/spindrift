package forge

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
)

// SwapCall records a single SwapLabel invocation.
type SwapCall struct {
	Num, Add, Remove string
}

// Fake is an in-memory Client for unit tests. All methods are safe for
// concurrent use. CheckState pops from a scripted RollupState queue so polling
// tests need no real sleeps.
type Fake struct {
	mu sync.Mutex

	issues    map[string]Issue
	prs       map[string]PR     // URL → PR
	branchPRs map[string]string // branch → PR URL
	prStates  map[string]string // URL → OPEN/MERGED/CLOSED
	checkQ    map[string][]RollupState

	// MergeErr, if non-nil, is returned by every Merge call.
	MergeErr error
	// Merged is set to the URL of the last successful Merge call.
	Merged string
	// SwapCalls records all SwapLabel invocations in order.
	SwapCalls []SwapCall
}

// NewFake returns an empty Fake client.
func NewFake() *Fake {
	return &Fake{
		issues:    map[string]Issue{},
		prs:       map[string]PR{},
		branchPRs: map[string]string{},
		prStates:  map[string]string{},
		checkQ:    map[string][]RollupState{},
	}
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
		f.prStates[pr.URL] = "OPEN"
	}
}

// SetPRState overrides the state (OPEN/MERGED/CLOSED) of a known PR.
func (f *Fake) SetPRState(url, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prStates[url] = state
}

// SetCheckStates scripts the sequence of RollupState values returned by
// successive CheckState calls for the given PR URL.
func (f *Fake) SetCheckStates(url string, states []RollupState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkQ[url] = append([]RollupState(nil), states...)
}

func (f *Fake) ListIssues(label string) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Issue
	for _, iss := range f.issues {
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

func (f *Fake) SwapLabel(num, add, remove string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SwapCalls = append(f.SwapCalls, SwapCall{num, add, remove})
	iss, ok := f.issues[num]
	if !ok {
		// Best-effort, just like the real gh CLI on a missing issue.
		return nil
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

func (f *Fake) OpenPRForBranch(branch string) (PR, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	url, ok := f.branchPRs[branch]
	if !ok {
		return PR{}, false, nil
	}
	pr, ok := f.prs[url]
	if !ok {
		return PR{}, false, nil
	}
	return pr, true, nil
}

func (f *Fake) PRState(url string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.prStates[url]
	if !ok {
		return "", fmt.Errorf("PR %s not found", url)
	}
	return s, nil
}

// CheckState pops the next scripted RollupState for url. When the queue is
// exhausted it returns StateNone (simulating a PR with no checks registered).
func (f *Fake) CheckState(url string) (RollupState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.checkQ[url]
	if len(q) == 0 {
		return StateNone, nil
	}
	s := q[0]
	f.checkQ[url] = q[1:]
	return s, nil
}

func (f *Fake) Merge(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MergeErr != nil {
		return f.MergeErr
	}
	f.Merged = url
	f.prStates[url] = "MERGED"
	return nil
}
