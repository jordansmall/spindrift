package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/waves"
)

// selectiveListDispatch dispatches a hand-picked list of issues. It bypasses the
// ready-for-agent label filter (operator override), but still honors real
// dependency edges: in-list blockers are ordered ahead; unmet external blockers
// trigger cascading eviction with a notice. Unlabeled issues print a warning and
// require a single batched confirmation before any Box is launched (skipped when
// forceYes=true or no unlabeled issues exist).
func selectiveListDispatch(c config, fc forge.Client, pwd string, f *dispatch.Factory, s settle.Settler, nums []string, forceYes bool, stdin io.Reader, stdout io.Writer) error {
	// Fetch each issue by number.
	issues, unlabeled, err := fetchSelectiveIssues(c, fc, nums)
	if err != nil {
		return err
	}

	// Warn for unlabeled issues and prompt once if any exist.
	if len(unlabeled) > 0 {
		for _, num := range unlabeled {
			fmt.Fprintf(stdout, "⚠ #%s not ready-for-agent; dispatching anyway (explicit)\n", num)
		}
		if !confirmUnlabeled(len(unlabeled), forceYes, stdin, stdout) {
			return fmt.Errorf("aborted: unlabeled issue(s) not confirmed")
		}
	}

	// Build blocker graph and evict dependents with unmet external blockers.
	edges, err := waves.BuildEdges(fc, toWaveIssues(issues))
	if err != nil {
		return err
	}

	issues, notices := evictUnmetBlockers(c, fc, issues, edges)
	for _, n := range notices {
		fmt.Fprintln(stdout, n)
	}

	if len(issues) == 0 {
		fmt.Fprintln(stdout, "no issues to dispatch after eviction")
		return nil
	}

	plan, err := waves.NewPlan(selectiveWavesConfig(c), waves.Input{Origin: waves.OriginSelective, Issues: toWaveIssues(issues), Edges: edges})
	if err != nil {
		return err
	}
	return waves.Run(selectiveWavesConfig(c), fc, pwd, f, s, plan)
}

// fetchSelectiveIssues fetches each issue by number and returns the full list
// plus the numbers of issues missing the ready-for-agent label.
func fetchSelectiveIssues(c config, fc forge.Client, nums []string) ([]issue, []string, error) {
	var issues []issue
	var unlabeled []string
	for _, num := range nums {
		fi, err := fc.Issue(num)
		if err != nil {
			return nil, nil, fmt.Errorf("issue %s: %w", num, err)
		}
		issues = append(issues, issue{number: fi.Number, title: fi.Title})
		if !containsLabel(fi.Labels, c.label) {
			unlabeled = append(unlabeled, fi.Number)
		}
	}
	return issues, unlabeled, nil
}

// confirmUnlabeled prints a single batched prompt and returns true if the
// operator confirms. Returns true immediately when forceYes=true. When stdin is
// not a terminal and forceYes=false the function returns false (non-interactive
// abort) rather than hanging.
func confirmUnlabeled(n int, forceYes bool, stdin io.Reader, stdout io.Writer) bool {
	if forceYes {
		return true
	}
	fmt.Fprintf(stdout, "Dispatch %d unlabeled issue(s)? [y/N] ", n)
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		// EOF / non-interactive
		fmt.Fprintln(stdout)
		return false
	}
	return strings.ToLower(strings.TrimSpace(scanner.Text())) == "y"
}

// evictUnmetBlockers removes issues whose unmerged blockers are absent from the
// list. Eviction cascades: if A is evicted, anything blocked by A is also
// evicted. Returns the retained issues and a notice string per evicted issue.
func evictUnmetBlockers(c config, fc forge.Client, issues []issue, edges map[string][]string) ([]issue, []string) {
	// willRun tracks which issue numbers are still candidates.
	willRun := make(map[string]bool, len(issues))
	for _, iss := range issues {
		willRun[iss.number] = true
	}

	var notices []string

	// blockerSatisfied returns true if the blocker is in willRun OR already done.
	blockerSatisfied := func(blocker string) bool {
		if willRun[blocker] {
			return true
		}
		return waves.BlockerReady(fc, blocker)
	}

	// Iterate the issues slice (not the map) to produce stable output order.
	for {
		var toEvict []string
		for _, iss := range issues {
			if !willRun[iss.number] {
				continue
			}
			for _, dep := range edges[iss.number] {
				if !blockerSatisfied(dep) {
					toEvict = append(toEvict, iss.number)
					break
				}
			}
		}
		if len(toEvict) == 0 {
			break
		}
		for _, num := range toEvict {
			notices = append(notices, fmt.Sprintf("⚠ #%s blocked by #%s (not in list, unmerged); skipping",
				num, firstUnmet(c, fc, willRun, edges[num])))
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

// firstUnmet returns the first entry in deps that is neither in willRun nor
// already satisfied (closed/complete). Used only for notice formatting.
func firstUnmet(c config, fc forge.Client, willRun map[string]bool, deps []string) string {
	for _, dep := range deps {
		if !willRun[dep] && !waves.BlockerReady(fc, dep) {
			return dep
		}
	}
	return "?"
}
