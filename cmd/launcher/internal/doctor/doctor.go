// Package doctor implements the forge/label validation shared by the
// `spindrift doctor` subcommand and Quickstart's finish line (ADR 0027).
// Quickstart runs before the CLI exists, so it cannot shell out to the
// subcommand and calls this package directly instead.
package doctor

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// ErrConnectivity classifies a Run failure as an auth-or-connectivity problem
// reaching the issue tracker or code forge (exit 3, issue #2569). Every builtin
// probe and label call wraps it with %w whatever the underlying cause, so a
// caller classifies by errors.Is instead of matching message text.
var ErrConnectivity = errors.New("issue tracker or code forge connectivity failure")

// ErrRequiredLabelsMissing classifies a Run failure as required checks failed or
// declined (exit 4, issue #2569): work-tier triage labels are missing and were
// not created.
var ErrRequiredLabelsMissing = errors.New("required triage label(s) missing or declined")

// errRequiredLabelsMissing is shared by the non-interactive and interactive-decline
// paths below so their identical message cannot drift apart.
func errRequiredLabelsMissing(workMissing []string) error {
	return fmt.Errorf("%w: %s missing — create them in the repository", ErrRequiredLabelsMissing, strings.Join(workMissing, ", "))
}

// labelMissingMsg is the message body shared by checkLabelSet's real "label
// %q missing" row and the quiet recap's hand-drawn copy of it, so the two
// cannot drift apart (issue #3777).
func labelMissingMsg(label string) string {
	return fmt.Sprintf("label %q missing", label)
}

// LabelMeta holds the default color and description for a triage label.
type LabelMeta struct {
	Description string
	Color       string // hex without leading #
}

// ResearchLabelNames returns the seven fixed research-tier label names (ADR 0022).
// All but "agent-research-finding" (ADR 0041) come from forge rather than local
// literals; that one has no forge declaration to source it from.
func ResearchLabelNames() []string {
	dl := forge.ResearchDispatchLabels()
	vl := forge.ResearchVerdictLabels()
	names := []string{dl.Dispatchable, dl.InProgress, dl.Failed}
	for _, e := range vl.Entries() {
		names = append(names, e.Label)
	}
	names = append(names, "agent-research-finding")
	return names
}

// PriorityLabelNames returns the three fixed priority-tier label names (ADR 0040).
func PriorityLabelNames() []string {
	return forge.PriorityLabelNames()
}

// AmbiguousLabelNames returns the single fixed ambiguous-spec-tier label name
// (issue #2275). The literal mirrors forge.DispatchLabels.Ambiguous, which
// has no accessor.
func AmbiguousLabelNames() []string {
	return []string{"agent-ambiguous-spec"}
}

// ButlerLabelNames returns the two fixed butler-tier label names, checked
// advisory the same way agent-research-finding is: the butler kind carries no
// lifecycle labels of its own (claims live in the Ledger), so these are its
// only doctor-visible labels. agent-butler-finding (ADR 0056) marks every
// Chore finding the host files; agent-butler-patch (ADR 0057) joins it on a
// finding the host lands as a patch PR.
func ButlerLabelNames() []string {
	return []string{"agent-butler-finding", "agent-butler-patch"}
}

// labelTier is one advisory label family. Only the work tier is Required, and
// its names come from Config, so it stays outside this table.
type labelTier struct {
	noun  string
	ref   string // ADR citation after the noun in report lines; may be empty
	names func() []string
}

// tierResult carries its tier alongside the missing names so consumers never
// index a parallel slice against advisoryTiers.
type tierResult struct {
	tier    labelTier
	missing []string
}

// advisoryTiers is the single place a tier's report metadata — noun, ref,
// names, and print order — lives. A new tier still needs a row in
// lib/labels.nix. nix/checks/dispatch-labels.nix only sees names written as a
// `return []string{"..."}` literal here; names sourced elsewhere (as priority's
// come from forge) escape that extraction silently.
var advisoryTiers = []labelTier{
	{noun: "research", ref: "(ADR 0022 / ADR 0041)", names: ResearchLabelNames},
	{noun: "priority", ref: "(ADR 0040)", names: PriorityLabelNames},
	{noun: "ambiguous-spec", ref: "", names: AmbiguousLabelNames},
	{noun: "butler", ref: "(ADR 0056)", names: ButlerLabelNames},
}

// AdvisoryLabelNames returns every advisory-tier label name in advisoryTiers
// order.
func AdvisoryLabelNames() []string {
	var names []string
	for _, t := range advisoryTiers {
		names = append(names, t.names()...)
	}
	return names
}

// refSuffix keeps an empty ref from leaving a double space in a report line.
func refSuffix(ref string) string {
	if ref == "" {
		return ""
	}
	return " " + ref
}

func allLabelsPresentMessage() string {
	nouns := []string{"triage"}
	for _, t := range advisoryTiers {
		nouns = append(nouns, t.noun)
	}
	last := len(nouns) - 1
	return "all " + strings.Join(nouns[:last], ", ") + ", and " + nouns[last] + " labels present"
}

// RuntimeCheckName is exported so callers filtering the row out of a larger slice
// match this constant, not the bare literal "runtime". Matching the literal would
// let a rename here silently reintroduce double-reporting of the runtime row.
const RuntimeCheckName = "runtime"

// RuntimeCheck builds the Required-tier "runtime" Check row backing
// launcherchecks.RequiredKnobChecks, which feeds validate()'s fatal startup gate.
// The advisory runtime line doctor and Quickstart print is a separate path
// (Config.Runtime), and both callers strip this row before calling Run so the two
// never both report for one invocation (issue #2559 AC2).
func RuntimeCheck(runtime string) Check {
	return Check{
		Name:   RuntimeCheckName,
		Tier:   Required,
		Remedy: "set RUNTIME to podman, docker, rancher, or bwrap, and ensure the matching CLI is on PATH",
		Probe: func() (any, error) {
			return nil, runner.ValidateRuntime(runtime)
		},
		SuccessMsg: func(output any) string {
			return fmt.Sprintf("runtime %q found on PATH", runtime)
		},
	}
}

// Config is the minimal slice of launcher config Run needs.
type Config struct {
	IssueTracker string

	// TokenHint and SlugHint name the env vars Run points an operator at in its
	// auth-failure and repo-not-found remediation text. The caller resolves them
	// because internal/doctor cannot see package main's backend registry, which
	// owns the backend-to-env-var mapping. Empty means the github-shaped default
	// (GH_TOKEN / --repo-slug REPO_SLUG).
	TokenHint string
	SlugHint  string

	Label           string
	InProgressLabel string
	FailedLabel     string
	CompleteLabel   string

	// Runtime is the configured container runtime (podman|docker|rancher|bwrap),
	// reported as advisory and never fatal: Quickstart's own prompt lets an
	// operator deliberately scaffold with an uninstalled runtime, so doctor must
	// not turn that accepted state into a hard failure.
	Runtime string

	// MergePolicy is the configured post-green merge policy (immediate|auto|manual,
	// MERGE_MODE). The branch-protection row is Required under immediate/auto,
	// which have no human merge gate, and Advisory under manual.
	MergePolicy string

	// BaseBranch is the branch the branch-protection row queries (BASE_BRANCH,
	// default "main").
	BaseBranch string
}

// Run probes the issue tracker and code forge, then checks that every configured
// work-tier label and every advisory tier in advisoryTiers exists, offering to
// create missing ones when interactive. Only missing work-tier labels fail the
// run; the other tiers and extraChecks are advisory. stdin is the caller's own
// scanner, so Quickstart can hand one over mid-flow without losing
// already-buffered input.
func Run(it forge.IssueTracker, cf forge.CodeForge, c Config, rep *Reporter, stdin *bufio.Scanner, interactive bool, extraChecks []Check) (err error) {
	tokenHint, slugHint := "GH_TOKEN", "--repo-slug / REPO_SLUG"
	if c.TokenHint != "" {
		tokenHint, slugHint = c.TokenHint, c.SlugHint
	}

	// it and cf are stable for this whole call, so BranchProtectionCheck and the
	// recoverable-issues probe share one resolution instead of each asserting the
	// optional interfaces themselves (issue #2946).
	caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})

	// These rows fail fast. Each Probe returns the repo slug so SuccessMsg can
	// name it.
	connectivityChecks := []Check{
		{
			Name: "issue-tracker",
			Tier: Required,
			Probe: func() (any, error) {
				repo, err := it.Probe()
				if err != nil {
					if errors.Is(err, forge.ErrAuthFailure) {
						return nil, fmt.Errorf("%w: forge auth check failed (check %s is set and valid): %w", ErrConnectivity, tokenHint, err)
					}
					if errors.Is(err, forge.ErrRepoNotFound) {
						return nil, fmt.Errorf("%w: forge repo not found (check %s is correct): %w", ErrConnectivity, slugHint, err)
					}
					if errors.Is(err, forge.ErrRateLimit) {
						return nil, fmt.Errorf("%w: forge rate limited (wait for the quota window to reset, then retry): %w", ErrConnectivity, err)
					}
					return nil, fmt.Errorf("%w: forge connectivity check failed: %w", ErrConnectivity, err)
				}
				return repo, nil
			},
			SuccessMsg: func(output any) string {
				return fmt.Sprintf("issue tracker confirmed — %s is reachable", output.(string))
			},
		},
		{
			Name: "code-forge",
			Tier: Required,
			Probe: func() (any, error) {
				repo, err := cf.Probe()
				if err != nil {
					return nil, fmt.Errorf("%w: code forge connectivity check failed: %w", ErrConnectivity, err)
				}
				return repo, nil
			},
			SuccessMsg: func(output any) string {
				return fmt.Sprintf("code forge confirmed — %s is reachable", output.(string))
			},
		},
	}

	// Unlike connectivityChecks above, a Required failure here must not block the
	// rest of Run (issue #2798): an unprotected base branch is a one-time repo
	// setup gap the operator fixes from the same run that surfaces it, not a sign
	// every later live call is moot the way an unreachable forge is.
	repoStateChecks := []Check{
		BranchProtectionCheck(caps, c.MergePolicy, c.BaseBranch),
		{
			Name: "recoverable-issues",
			Tier: Required,
			Probe: func() (any, error) {
				// Query only when Recoverable resolves to a real label. GitHub
				// and Forgejo ignore an empty label filter instead of erroring,
				// so an unconditional call would match every open issue on a
				// tracker that leaves Recoverable unmapped. console/adapter.go's
				// countRecoverable guards the same way.
				recoverableCount := 0
				if caps.LabeledTracker == nil || caps.LabeledTracker.StateLabels().Label(forge.Recoverable) != "" {
					recoverable, err := it.ListIssues(forge.Recoverable)
					if err != nil {
						return nil, fmt.Errorf("%w: recoverable issue check failed: %w", ErrConnectivity, err)
					}
					recoverableCount = len(recoverable)
				}
				return recoverableCount, nil
			},
			SuccessMsg: func(output any) string {
				return fmt.Sprintf("%d recoverable issue(s) — run `spindrift recover <issue>` to land each", output.(int))
			},
		},
	}

	results := RunChecksFailFast(connectivityChecks)
	if cerr := FirstRequiredError(results); cerr != nil {
		// RunChecksFailFast stops at the first Required failure, so that result is
		// always the last element. cmdDoctor already prints cerr to stderr, so
		// writing the failing row's MISSING line here too would double-report it.
		// It never prints the Remedy, so write that one line or the remedy reaches
		// the operator nowhere.
		rep.Results(results[:len(results)-1])
		failing := results[len(results)-1]
		rep.remedyLine(failing.Check.Remedy, cerr.Error())
		return cerr
	}
	rep.Results(results)

	// A blocking repository-state failure is reported inline, unlike the failing
	// connectivity row above: the report continues past it, so suppressing the
	// MISSING line would leave an orphan remedy line under no row at all.
	repoStateResults := RunChecks(repoStateChecks)
	rep.Results(repoStateResults)
	deferredRepoStateErr := FirstRequiredError(repoStateResults)
	defer func() {
		// This error wins the return value, so a later error from the label
		// section below would otherwise be overwritten and reach no stream at
		// all. Print it to w first.
		if deferredRepoStateErr != nil {
			if err != nil {
				rep.Finding(Required, "%v", err)
			}
			err = deferredRepoStateErr
		}
	}()

	// extraChecks are informational: a failing row at either tier never makes Run
	// return an error.
	rep.Results(RunChecks(extraChecks))

	// Runtime row, advisory and never fatal. Rationale on Config.Runtime.
	if c.Runtime == "" {
		rep.Finding(Advisory, "RUNTIME not set — skipping runtime check")
	} else if rerr := runner.ValidateRuntime(c.Runtime); rerr != nil {
		rep.Finding(Advisory, "runtime %q not ready: %v — does not fail this check", c.Runtime, rerr)
	} else {
		rep.Success("runtime %q found on PATH", c.Runtime)
	}

	// A missing row's prefix mirrors its tier's exit-code weight: MISSING for the
	// fatal work tier, advisory for the three tiers that never fail the check.
	checkLabelSet := func(names []string, present map[string]bool, tier Tier) []string {
		var missing []string
		for _, label := range names {
			if present[label] {
				rep.Success("label %q present", label)
				continue
			}
			rep.Finding(tier, "%s", labelMissingMsg(label))
			missing = append(missing, label)
		}
		return missing
	}

	// checkLabels reports on the work tier plus every advisoryTiers entry, but
	// only the work tier is fatal — the advisory tiers stay non-fatal so a CI
	// doctor run stays green on a deployment that does not use them yet.
	checkLabels := func() (workMissing []string, tierResults []tierResult, err error) {
		existing, lerr := it.ListLabels()
		if lerr != nil {
			return nil, nil, fmt.Errorf("%w: label check failed: %w", ErrConnectivity, lerr)
		}
		present := make(map[string]bool, len(existing))
		for _, l := range existing {
			present[l] = true
		}
		workMissing = checkLabelSet([]string{c.Label, c.InProgressLabel, c.FailedLabel, c.CompleteLabel}, present, Required)
		tierResults = make([]tierResult, len(advisoryTiers))
		for i, t := range advisoryTiers {
			tierResults[i] = tierResult{tier: t, missing: checkLabelSet(t.names(), present, Advisory)}
		}
		return workMissing, tierResults, nil
	}

	workMissing, tierResults, err := checkLabels()
	if err != nil {
		return err
	}
	var advisoryMissing []string
	for _, tr := range tierResults {
		if len(tr.missing) > 0 {
			rep.Finding(Advisory, "%d %s label(s) missing%s — does not fail this check", len(tr.missing), tr.tier.noun, refSuffix(tr.tier.ref))
		}
		advisoryMissing = append(advisoryMissing, tr.missing...)
	}
	missing := append(append([]string{}, workMissing...), advisoryMissing...)
	if len(missing) == 0 {
		rep.Success("%s", allLabelsPresentMessage())
		return nil
	}

	if !interactive {
		if len(workMissing) > 0 {
			return errRequiredLabelsMissing(workMissing)
		}
		return nil
	}

	advisoryCount := len(advisoryMissing)
	requiredClause := fmt.Sprintf("%d required", len(workMissing))
	if len(workMissing) > 0 {
		requiredClause += " (declining leaves this check failing)"
	}
	advisoryClause := fmt.Sprintf("%d advisory", advisoryCount)
	if advisoryCount > 0 {
		advisoryClause += " (declining is safe, does not fail this check)"
	}
	// Quiet suppresses the advisory Finding rows above, but the prompt still
	// offers to create those labels — recap them here so an operator never
	// approves creating a label they were never shown (issue #3777 AC5). Gated
	// on rep.verbose so the verbose report, which already printed these rows,
	// stays byte-for-byte unchanged.
	if !rep.verbose {
		for _, label := range advisoryMissing {
			rep.Passthrough("%s: %s\n", rowPrefix(Advisory), labelMissingMsg(label))
		}
	}
	rep.Passthrough("Create %d missing label(s) — %s and %s? [y/N] ",
		len(missing), requiredClause, advisoryClause)
	if !stdin.Scan() || strings.ToLower(strings.TrimSpace(stdin.Text())) != "y" {
		rep.Passthrough("\n")
		if len(workMissing) > 0 {
			return errRequiredLabelsMissing(workMissing)
		}
		return nil
	}

	// metaFor resolves the four work-tier labels by role because an operator can
	// rename them: a TriageLabelMeta[name] lookup keyed on the default name would
	// miss a renamed label and fall back to gray (#2528 AC2). The other tiers use
	// fixed literals, so the map lookup stays correct for them.
	metaFor := func(name string) LabelMeta {
		switch name {
		case c.Label:
			return MetaDispatchable
		case c.InProgressLabel:
			return MetaInProgress
		case c.FailedLabel:
			return MetaFailed
		case c.CompleteLabel:
			return MetaComplete
		}
		if meta, ok := TriageLabelMeta[name]; ok {
			return meta
		}
		return LabelMeta{Color: "ededed"}
	}

	// A CreateLabel failure on a required work-tier label is fatal. One on an
	// advisory label is only reported, here and again in the still-missing lines
	// below: accepting the prompt must never leave an operator worse off than
	// declining it, which is safe for an advisory-only run (issue #2569).
	workSet := make(map[string]bool, len(workMissing))
	for _, name := range workMissing {
		workSet[name] = true
	}
	for _, name := range missing {
		meta := metaFor(name)
		if cerr := it.CreateLabel(name, meta.Description, meta.Color); cerr != nil {
			if workSet[name] {
				return fmt.Errorf("%w: create label %q: %w", ErrConnectivity, name, cerr)
			}
			rep.Finding(Advisory, "create label %q failed: %v — does not fail this check", name, cerr)
			continue
		}
		rep.Passthrough("created: label %q\n", name)
	}

	workMissing, tierResults, err = checkLabels()
	if err != nil {
		return err
	}
	if len(workMissing) > 0 {
		return fmt.Errorf("%w: %s still missing after creation", ErrRequiredLabelsMissing, strings.Join(workMissing, ", "))
	}
	// Work labels are fatal above, so each advisory tier gets its own wrap-up
	// line here, or one success line naming every tier when none is still
	// short.
	stillMissing := false
	// These lines carry the "advisory:" prefix like a Finding, but they
	// report a CreateLabel outcome that already happened rather than a fresh
	// probe result, so the acceptance criteria class them as always-on
	// passthrough instead of routing them through Finding's tier gate.
	for _, tr := range tierResults {
		if len(tr.missing) > 0 {
			rep.Passthrough("advisory: %d %s label(s) still missing after creation%s — does not fail this check: %s\n", len(tr.missing), tr.tier.noun, refSuffix(tr.tier.ref), strings.Join(tr.missing, ", "))
			stillMissing = true
		}
	}
	if stillMissing {
		return nil
	}
	rep.Success("%s", allLabelsPresentMessage())
	return nil
}
