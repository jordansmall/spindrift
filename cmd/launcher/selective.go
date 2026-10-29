package main

import (
	"bufio"
	"errors"
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
// interactive reports whether stdin is a terminal; without one, unlabeled
// issues need forceYes.
func selectiveListDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, pwd string, f *dispatch.Factory, s settle.Settler, nums []string, forceYes, interactive bool, stdin io.Reader, stdout io.Writer) error {
	// Installed once, ahead of the first fetch, the same placement run uses
	// (#3522): every pre-wave early return below checks
	// waves.SignalledStopAlready so a signal that already fired wins over any
	// of them.
	stopCh, abortCh, stopCleanup := installStopSignal()
	defer stopCleanup()

	issues, unlabeled, repeated, err := fetchSelectiveIssues(c, it, nums)
	if err != nil {
		return signalledOr(stopCh, abortCh, err)
	}

	for _, num := range repeated {
		fmt.Fprintf(stdout, "⚠ #%s listed more than once; dispatching once\n", num)
	}

	if len(unlabeled) > 0 {
		for _, num := range unlabeled {
			fmt.Fprintf(stdout, "⚠ #%s not ready-for-agent; dispatching anyway (explicit)\n", num)
		}
		if err := confirmUnlabeled(len(unlabeled), forceYes, interactive, stdin, stdout); err != nil {
			return signalledOr(stopCh, abortCh, err)
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
// missing the ready-for-agent label. A number the tracker resolves to an
// issue already fetched is dropped, first occurrence winning, since a repeat
// would claim and run that issue twice; the dropped numbers come back as
// repeated so callers can say so.
func fetchSelectiveIssues(c config, it forge.IssueTracker, nums []string) (issues []issue, unlabeled, repeated []string, err error) {
	seen := make(map[string]bool, len(nums))
	for _, num := range nums {
		fi, err := it.Issue(num)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("issue %s: %w", num, err)
		}
		if seen[fi.Number] {
			repeated = append(repeated, fi.Number)
			continue
		}
		seen[fi.Number] = true
		issues = append(issues, newIssue(fi))
		if !containsLabel(fi.Labels, c.label) {
			unlabeled = append(unlabeled, fi.Number)
		}
	}
	return issues, unlabeled, repeated, nil
}

var errUnlabeledNotConfirmed = errors.New("aborted: unlabeled issue(s) not confirmed")

// confirmUnlabeled prompts once for all n issues and returns nil when
// confirmed. Without a terminal and without forceYes it errors without
// reading stdin, so a piped "y" never confirms and an open pipe never blocks.
func confirmUnlabeled(n int, forceYes, interactive bool, stdin io.Reader, stdout io.Writer) error {
	if forceYes {
		return nil
	}
	if !interactive {
		return fmt.Errorf("%w (stdin is not a terminal; pass --yes to dispatch them)", errUnlabeledNotConfirmed)
	}
	fmt.Fprintf(stdout, "Dispatch %d unlabeled issue(s)? [y/N] ", n)
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		fmt.Fprintln(stdout)
		return errUnlabeledNotConfirmed
	}
	if strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
		return errUnlabeledNotConfirmed
	}
	return nil
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
