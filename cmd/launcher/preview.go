package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/waves"
)

// previewIssues is the testable core of the preview verb. The freshness line
// already folds in the launcher dimension when c.flakeLauncherAttr is set
// (issue #1364), so the launcher-currency-attr line below it only echoes
// config and is not a second verdict.
func previewIssues(c config, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, w io.Writer, issueNums []string, pwd string, eval freshness.Evaluator) error {
	res := freshness.Probe(freshness.ProbeSpec{
		RunnerKind:         c.runnerKind,
		Pwd:                pwd,
		BaseBranch:         c.baseBranch,
		FlakeImageAttr:     c.flakeImageAttr,
		ImageTag:           c.imageTag,
		FlakeLauncherAttr:  c.flakeLauncherAttr,
		LoadedLauncherHash: c.loadedLauncherHash,
	}, eval)
	fmt.Fprintf(w, "freshness: %s\n", res.Message)
	launcherAttr := c.flakeLauncherAttr
	if launcherAttr == "" {
		launcherAttr = "(unset)"
	}
	fmt.Fprintf(w, "launcher-currency-attr: %s\n", launcherAttr)

	if len(issueNums) > 0 {
		return previewSelectiveList(c, it, cf, caps, w, issueNums)
	}

	issues, origin, err := discoverIssues(c, it)
	if err != nil {
		return err
	}
	if origin == waves.OriginDiscovered && len(issues) == 0 {
		fmt.Fprintf(w, "%s\nno open '%s' issues — nothing to dispatch.\n", repoBanner(c), c.label)
		return nil
	}
	result, err := waves.NewReadiness(it, toWaveIssues(issues))
	if err != nil {
		return err
	}
	plan, err := waves.NewPlan(wavesConfig(c), waves.NewInput(origin, result, toWaveIssues(issues)))
	if err != nil {
		return err
	}
	fmt.Fprintln(w, repoBanner(c))
	printPlan(w, plan)
	return nil
}

// previewSelectiveList dry-runs the selective-list dispatch path: it starts no
// Box, prompts for nothing, and makes no forge mutations.
func previewSelectiveList(c config, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, w io.Writer, nums []string) error {
	issues, unlabeled, err := fetchSelectiveIssues(c, it, nums)
	if err != nil {
		return err
	}

	for _, num := range unlabeled {
		fmt.Fprintf(w, "⚠ #%s not ready-for-agent; dispatching anyway (explicit)\n", num)
	}

	readiness, err := waves.NewReadiness(it, toWaveIssues(issues))
	if err != nil {
		return err
	}

	kept, notices := evictUnmetBlockers(it, cf, caps, readiness, issues)
	for _, n := range notices {
		fmt.Fprintln(w, n)
	}

	fmt.Fprintln(w, repoBanner(c))
	if len(kept) == 0 {
		fmt.Fprintf(w, "no issues would be dispatched after eviction\n")
		return nil
	}
	plan, err := waves.NewPlan(selectiveWavesConfig(c), waves.NewInput(waves.OriginSelective, readiness, toWaveIssues(kept)))
	if err != nil {
		return err
	}
	printPlan(w, plan)
	return nil
}

// printPlan renders a Plan's dispatch list for both preview paths, so the
// blocked-by annotation loop exists exactly once.
func printPlan(w io.Writer, plan waves.Plan) {
	fmt.Fprintf(w, "%d issue(s) would be dispatched:\n", len(plan.Issues))
	for _, iss := range plan.Issues {
		blockers := plan.Edges[iss.Number]
		switch {
		case len(blockers) > 0:
			refs := make([]string, len(blockers))
			for i, b := range blockers {
				refs[i] = forge.Ref(b, plan.Sources[iss.Number][b])
			}
			fmt.Fprintf(w, "  #%s  %s  (blocked by %s)\n", iss.Number, iss.Title, strings.Join(refs, ", "))
		case plan.Failed[iss.Number]:
			fmt.Fprintf(w, "  #%s  %s  (blocker check failed; will retry)\n", iss.Number, iss.Title)
		default:
			fmt.Fprintf(w, "  #%s  %s\n", iss.Number, iss.Title)
		}
	}
}

func preview(issueNums []string) error {
	// Preview never dispatches, so it carries no dispatch kind at all rather
	// than dispatchKindWork, matching doctor and reconcile (issue #2944).
	gc, err := newGatedContext(os.Stdout, "", false)
	if err != nil {
		return err
	}
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return previewIssues(gc.config, gc.issueTracker, gc.codeForge, gc.capabilities, os.Stdout, issueNums, pwd, runner.NixEvaluator{})
}
