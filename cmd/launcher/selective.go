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
	"spindrift.dev/launcher/internal/terminate"
	"spindrift.dev/launcher/internal/waves"
)

// selectiveListDispatch dispatches a hand-picked list of issues, bypassing the
// ready-for-agent label filter as an operator override. Dependency edges still
// hold: in-list blockers are ordered ahead, and unmet external blockers evict
// dependents. caps is safe to reuse because cf is fixed for the whole call
// (issue #2946). Without an interactive stdin, unlabeled or off-route issues
// need forceYes.
func selectiveListDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, pwd string, f *dispatch.Factory, s settle.Settler, terminated *terminate.Registry, nums []string, forceYes, interactive bool, stdin io.Reader, stdout io.Writer) error {
	// Installed once, ahead of the first fetch, as run does (#3522), so a
	// signal that already fired wins over every pre-wave early return below.
	stopCh, abortCh, stopCleanup := installStopSignal()
	defer stopCleanup()

	fetched, err := fetchSelectiveIssues(c, it, caps, nums)
	if err != nil {
		return signalledOr(stopCh, abortCh, err)
	}
	issues := fetched.issues

	fetched.warn(stdout, c.IssueRoute)

	if len(fetched.unlabeled)+len(fetched.offRoute) > 0 {
		if err := confirmExplicit(fetched.unlabeled, fetched.offRoute, forceYes, interactive, stdin, stdout); err != nil {
			return signalledOr(stopCh, abortCh, err)
		}
	}

	readiness, err := waves.NewReadiness(it, toWaveIssues(issues))
	if err != nil {
		return signalledOr(stopCh, abortCh, err)
	}

	issues, notices := evictUnmetBlockers(it, caps, c.BranchPrefix, readiness, issues)
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
	claimer := waves.NewLabelClaimer(it, c.Label, c.InProgressLabel)
	return waves.Dispatch(cfg, &waves.Session{Terminated: terminated}, it, cf, pwd, f, s, in, claimer)
}

// selectiveFetch is the result of fetchSelectiveIssues.
type selectiveFetch struct {
	issues                        []issue
	unlabeled, repeated, offRoute []string
}

// warn prints the explicit-override warnings both selective paths share.
func (f selectiveFetch) warn(w io.Writer, route string) {
	for _, num := range f.repeated {
		fmt.Fprintf(w, "⚠ #%s listed more than once; dispatching once\n", num)
	}
	for _, num := range f.unlabeled {
		fmt.Fprintf(w, "⚠ #%s not ready-for-agent; dispatching anyway (explicit)\n", num)
	}
	for _, num := range f.offRoute {
		fmt.Fprintf(w, "⚠ #%s outside the issue route (%s); dispatching anyway (explicit)\n", num, route)
	}
}

// fetchSelectiveIssues returns the fetched issues plus the numbers of those
// missing the ready-for-agent label and, when ISSUE_ROUTE is set, of those
// outside it. A number the tracker resolves to an issue already fetched is
// dropped, first occurrence winning, since a repeat would claim and run that
// issue twice; the dropped numbers come back as repeated so callers can say so.
// With no route set no route check is made at all, so a tracker without a
// RouteMatcher is untouched; with one set, a tracker lacking it is an error
// rather than a silent pass.
func fetchSelectiveIssues(c config, it forge.IssueTracker, caps forge.Capabilities, nums []string) (selectiveFetch, error) {
	var out selectiveFetch
	var matcher forge.RouteMatcher
	if len(issueRoute(c)) > 0 {
		if matcher = caps.RouteMatcher; matcher == nil {
			return selectiveFetch{}, fmt.Errorf("ISSUE_ROUTE %q is set but the issue tracker cannot check an issue against it", c.IssueRoute)
		}
	}
	seen := make(map[string]bool, len(nums))
	for _, num := range nums {
		fi, err := it.Issue(num)
		if err != nil {
			return selectiveFetch{}, fmt.Errorf("issue %s: %w", num, err)
		}
		if seen[fi.Number] {
			out.repeated = append(out.repeated, fi.Number)
			continue
		}
		seen[fi.Number] = true
		out.issues = append(out.issues, newIssue(fi))
		if !containsLabel(fi.Labels, c.Label) {
			out.unlabeled = append(out.unlabeled, fi.Number)
		}
		if matcher != nil {
			ok, err := matcher.MatchesRoute(fi.Number)
			if err != nil {
				return selectiveFetch{}, fmt.Errorf("issue %s: route check: %w", num, err)
			}
			if !ok {
				out.offRoute = append(out.offRoute, fi.Number)
			}
		}
	}
	return out, nil
}

var (
	errUnlabeledNotConfirmed = errors.New("aborted: unlabeled issue(s) not confirmed")
	errExplicitNotConfirmed  = errors.New("aborted: unlabeled or off-route issue(s) not confirmed")
)

// confirmExplicit prompts once for every issue dispatched by explicit override,
// counting one that is both unlabeled and off-route once, and returns nil when
// confirmed. Without a terminal and without forceYes it errors without
// reading stdin, so a piped "y" never confirms and an open pipe never blocks.
func confirmExplicit(unlabeled, offRoute []string, forceYes, interactive bool, stdin io.Reader, stdout io.Writer) error {
	notConfirmed := errExplicitNotConfirmed
	if len(offRoute) == 0 {
		notConfirmed = errUnlabeledNotConfirmed
	}
	if forceYes {
		return nil
	}
	if !interactive {
		return fmt.Errorf("%w (stdin is not a terminal; pass --yes to dispatch them)", notConfirmed)
	}
	distinct := make(map[string]bool, len(unlabeled)+len(offRoute))
	for _, num := range unlabeled {
		distinct[num] = true
	}
	for _, num := range offRoute {
		distinct[num] = true
	}
	if len(offRoute) == 0 {
		fmt.Fprintf(stdout, "Dispatch %d unlabeled issue(s)? [y/N] ", len(distinct))
	} else {
		refs := make([]string, len(offRoute))
		for i, num := range offRoute {
			refs[i] = "#" + num
		}
		fmt.Fprintf(stdout, "Dispatch %d unlabeled or off-route issue(s) (off-route: %s)? [y/N] ", len(distinct), strings.Join(refs, ", "))
	}
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		fmt.Fprintln(stdout)
		return notConfirmed
	}
	if strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
		return notConfirmed
	}
	return nil
}

// evictUnmetBlockers removes issues whose unmerged blockers are absent from the
// list. Eviction cascades: anything blocked by an evicted issue is evicted too.
func evictUnmetBlockers(it forge.IssueTracker, caps forge.Capabilities, branchPrefix string, readiness waves.Readiness, issues []issue) ([]issue, []string) {
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
		return readiness.Ready(it, caps, branchPrefix, blocker, seedScopeFor(dependent))
	}

	// Iterate the issues slice, not the map, for stable output order.
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
			dep := firstUnmet(it, caps, branchPrefix, readiness, willRun, num, readiness.Edges[num], seedScopeFor)
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
func firstUnmet(it forge.IssueTracker, caps forge.Capabilities, branchPrefix string, readiness waves.Readiness, willRun map[string]bool, dependent string, deps []string, seedScopeFor func(string) forge.SeedScope) string {
	for _, dep := range deps {
		if !willRun[dep] && !readiness.Ready(it, caps, branchPrefix, dep, seedScopeFor(dependent)) {
			return dep
		}
	}
	return "?"
}
