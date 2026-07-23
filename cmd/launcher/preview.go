package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/waves"
)

// previewIssues is the testable core of the preview verb. It always prints
// an image-freshness line first (freshness.Probe against pwd/eval), then:
// when issueNums is non-empty it performs a selective dry-run — fetches
// exactly those issues, prints label-bypass warnings, blocker annotations,
// and cascade-eviction notices without launching any Box or prompting; when
// issueNums is empty it falls back to queue-drain discovery.
func previewIssues(c config, it forge.IssueTracker, cf forge.CodeForge, w io.Writer, issueNums []string, pwd string, eval freshness.Evaluator) error {
	res := freshness.Probe(c.runtime, pwd, c.baseBranch, c.flakeImageAttr, c.imageTag, eval)
	fmt.Fprintf(w, "image-freshness: %s\n", res.Message)

	if len(issueNums) > 0 {
		return previewSelectiveList(c, it, cf, w, issueNums)
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
	plan, err := waves.NewPlan(wavesConfig(c), waves.Input{Origin: origin, Issues: toWaveIssues(issues), Edges: result.Edges, Sources: result.Sources, Failed: result.Failed})
	if err != nil {
		return err
	}
	fmt.Fprintln(w, repoBanner(c))
	printPlan(w, plan)
	return nil
}

// previewSelectiveList performs a dry-run of the selective-list dispatch path.
// It prints label-bypass warnings, per-issue blocker annotations, and cascade-
// eviction notices. No Boxes are started and no Forge mutations occur.
func previewSelectiveList(c config, it forge.IssueTracker, cf forge.CodeForge, w io.Writer, nums []string) error {
	issues, unlabeled, err := fetchSelectiveIssues(c, it, nums)
	if err != nil {
		return err
	}

	// Label-bypass warnings (no prompt in preview).
	for _, num := range unlabeled {
		fmt.Fprintf(w, "⚠ #%s not ready-for-agent; dispatching anyway (explicit)\n", num)
	}

	// Parse blocker graph.
	readiness, err := waves.NewReadiness(it, toWaveIssues(issues))
	if err != nil {
		return err
	}
	edges, sources, failed := readiness.Edges, readiness.Sources, readiness.Failed

	// Eviction pass (dry-run; no side effects).
	kept, notices := evictUnmetBlockers(it, cf, readiness, issues)
	for _, n := range notices {
		fmt.Fprintln(w, n)
	}

	fmt.Fprintln(w, repoBanner(c))
	if len(kept) == 0 {
		fmt.Fprintf(w, "no issues would be dispatched after eviction\n")
		return nil
	}
	plan, err := waves.NewPlan(selectiveWavesConfig(c), waves.Input{Origin: waves.OriginSelective, Issues: toWaveIssues(kept), Edges: edges, Sources: sources, Failed: failed})
	if err != nil {
		return err
	}
	printPlan(w, plan)
	return nil
}

// printPlan is the single shared renderer for a Plan's dispatch list, used by
// both the discovered-batch and selective preview paths so the blocked-by
// annotation loop exists exactly once.
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

// preview is the entry point for the `preview` subcommand.
func preview(issueNums []string) error {
	c := loadConfig()
	if err := validate(c); err != nil {
		return err
	}
	it := newIssueTracker(c)
	cf := newCodeForge(c, local.SanitizedParent{})
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return previewIssues(c, it, cf, os.Stdout, issueNums, pwd, runner.NixEvaluator{})
}
