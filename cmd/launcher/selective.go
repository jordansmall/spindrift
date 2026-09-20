package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/waves"
)

// selectiveListDispatch dispatches a hand-picked list of issues, bypassing the
// ready-for-agent label filter as an operator override. Dependency edges still
// hold: in-list blockers are ordered ahead, and unmet external blockers evict
// dependents. caps is the caller's resolved forge.Capabilities (issue #2946),
// safe to reuse because cf is fixed for this whole call.
func selectiveListDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, pwd string, f *dispatch.Factory, s settle.Settler, nums []string, forceYes bool, stdin io.Reader, stdout io.Writer) error {
	// Installed once, ahead of the first fetch, the same placement run uses
	// (#3522): every pre-wave early return below checks
	// waves.SignalledStopAlready so a signal that already fired wins over any
	// of them.
	stopCh, abortCh, stopCleanup := installStopSignal()
	defer stopCleanup()

	issues, unlabeled, err := fetchSelectiveIssues(c, it, nums)
	if err != nil {
		return signalledOr(stopCh, abortCh, err)
	}

	if len(unlabeled) > 0 {
		for _, num := range unlabeled {
			fmt.Fprintf(stdout, "⚠ #%s not ready-for-agent; dispatching anyway (explicit)\n", num)
		}
		if !confirmUnlabeled(len(unlabeled), forceYes, stdin, stdout) {
			return signalledOr(stopCh, abortCh, fmt.Errorf("aborted: unlabeled issue(s) not confirmed"))
		}
	}

	readiness, err := waves.NewReadiness(it, toWaveIssues(issues))
	if err != nil {
		return signalledOr(stopCh, abortCh, err)
	}

	issues, notices := evictUnmetBlockers(it, cf, caps, readiness, issues)
	for _, n := range notices {
		fmt.Fprintln(stdout, n)
	}

	if len(issues) == 0 {
		if waves.SignalledStopAlready(stopCh, abortCh) {
			return waves.ErrSignalledStop
		}
		fmt.Fprintln(stdout, "no issues to dispatch after eviction")
		return nil
	}

	in := waves.NewInput(waves.OriginSelective, readiness, toWaveIssues(issues))
	cfg := selectiveWavesConfig(c)
	cfg.SeedScopeOf = localloop.SeedScopeResolver(it, caps)
	cfg.Stop = stopCh
	cfg.Abort = abortCh
	claimer := waves.NewLabelClaimer(it, c.label, c.inProgressLabel)
	terminated := registryFor(s)
	return waves.Dispatch(cfg, &waves.Session{Terminated: terminated}, it, cf, pwd, f, s, in, claimer)
}

// fetchSelectiveIssues returns the fetched issues plus the numbers of those
// missing the ready-for-agent label.
func fetchSelectiveIssues(c config, it forge.IssueTracker, nums []string) ([]issue, []string, error) {
	var issues []issue
	var unlabeled []string
	for _, num := range nums {
		fi, err := it.Issue(num)
		if err != nil {
			return nil, nil, fmt.Errorf("issue %s: %w", num, err)
		}
		issues = append(issues, newIssue(fi))
		if !containsLabel(fi.Labels, c.label) {
			unlabeled = append(unlabeled, fi.Number)
		}
	}
	return issues, unlabeled, nil
}

// confirmUnlabeled prompts once for all n issues. When stdin is not a terminal
// and forceYes is false it returns false rather than hanging.
func confirmUnlabeled(n int, forceYes bool, stdin io.Reader, stdout io.Writer) bool {
	if forceYes {
		return true
	}
	fmt.Fprintf(stdout, "Dispatch %d unlabeled issue(s)? [y/N] ", n)
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		fmt.Fprintln(stdout)
		return false
	}
	return strings.ToLower(strings.TrimSpace(scanner.Text())) == "y"
}

// evictUnmetBlockers removes issues whose unmerged blockers are absent from the
// list. Eviction cascades: anything blocked by an evicted issue is evicted too.
func evictUnmetBlockers(it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, readiness waves.Readiness, issues []issue) ([]issue, []string) {
	willRun := make(map[string]bool, len(issues))
	for _, iss := range issues {
		willRun[iss.number] = true
	}

	var notices []string

	// Only the local forge resolves a dependent's SeedScope (#2130). Elsewhere
	// resolve is nil, so the seed-branch containment gate never fires and a
	// blocker is judged solely by its PR/issue state.
	resolve := localloop.SeedScopeResolver(it, caps)
	seedScopeFor := func(dependent string) forge.SeedScope {
		if resolve == nil {
			return forge.SeedScope{}
		}
		return resolve(dependent)
	}

	blockerSatisfied := func(dependent, blocker string) bool {
		if willRun[blocker] {
			return true
		}
		return readiness.Ready(it, cf, caps, blocker, seedScopeFor(dependent))
	}

	// Iterate the issues slice (not the map) to produce stable output order.
	for {
		var toEvict []string
		for _, iss := range issues {
			if !willRun[iss.number] {
				continue
			}
			for _, dep := range readiness.Edges[iss.number] {
				if !blockerSatisfied(iss.number, dep) {
					toEvict = append(toEvict, iss.number)
					break
				}
			}
		}
		if len(toEvict) == 0 {
			break
		}
		for _, num := range toEvict {
			dep := firstUnmet(it, cf, caps, readiness, willRun, num, readiness.Edges[num], seedScopeFor)
			notices = append(notices, fmt.Sprintf("⚠ #%s blocked by %s (not in list, unmerged); skipping",
				num, forge.Ref(dep, readiness.Sources[num][dep])))
			delete(willRun, num)
		}
	}

	var kept []issue
	for _, iss := range issues {
		if willRun[iss.number] {
			kept = append(kept, iss)
		}
	}
	return kept, notices
}

// firstUnmet returns the first dep that is neither in willRun nor already
// satisfied, relative to dependent. Used only for notice formatting.
func firstUnmet(it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, readiness waves.Readiness, willRun map[string]bool, dependent string, deps []string, seedScopeFor func(string) forge.SeedScope) string {
	for _, dep := range deps {
		if !willRun[dep] && !readiness.Ready(it, cf, caps, dep, seedScopeFor(dependent)) {
			return dep
		}
	}
	return "?"
}
